package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/r2"
	"github.com/rosskukulinski/tailworks/mgmt"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// counterJS is a Durable Object that, in each write, appends a row to log
// and sets c.n to the row count in one transaction: any consistent copy has
// count(log) = max(log.n) = c.n, and a torn one doesn't (or isn't a
// database at all). Rows are padded so a write touches several pages.
const counterJS = `
export class Counter {
  constructor(ctx) {
    this.ctx = ctx;
    this.sql = ctx.storage.sql;
    this.sql.exec("CREATE TABLE IF NOT EXISTS c (n INTEGER NOT NULL)");
    this.sql.exec("CREATE TABLE IF NOT EXISTS log (n INTEGER PRIMARY KEY, pad TEXT NOT NULL)");
    if (this.sql.exec("SELECT count(*) AS k FROM c").one().k === 0) this.sql.exec("INSERT INTO c VALUES (0)");
  }
  fetch(req) {
    if (new URL(req.url).pathname === "/w") {
      let n = 0;
      this.ctx.storage.transactionSync(() => {
        n = this.sql.exec("SELECT n FROM c").one().n + 1;
        this.sql.exec("INSERT INTO log VALUES (?, ?)", n, "x".repeat(3000 + (n % 7) * 500));
        this.sql.exec("UPDATE c SET n = ?", n);
      });
      return new Response(String(n));
    }
    return new Response(this.ctx.id.toString() + " " + this.sql.exec("SELECT n FROM c").one().n);
  }
}
export default {
  fetch(req, env) {
    const o = new URL(req.url).searchParams.get("o");
    return env.C.get(env.C.idFromName(o)).fetch(req);
  },
};
`

// startCounterWorkerd runs workerd with counterJS, its DO storage in doDir
// (as hostd's gen does: a writable disk service, localDisk storage).
func startCounterWorkerd(t *testing.T, wd, doDir string) (base string, stop func()) {
	t.Helper()
	dir := t.TempDir()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	os.MkdirAll(doDir, 0o700)
	cfg := fmt.Sprintf(`using Workerd = import "/workerd/workerd.capnp";
const config :Workerd.Config = (
  services = [
    (name = "main", worker = .w),
    (name = "do-storage", disk = (path = %q, writable = true)),
  ],
  sockets = [(name = "http", address = %q, http = (), service = "main")],
);
const w :Workerd.Worker = (
  compatibilityDate = "2026-04-01",
  modules = [(name = "index.js", esModule = embed "index.js")],
  durableObjectNamespaces = [(className = "Counter", uniqueKey = "tw-app-Counter", enableSql = true)],
  durableObjectStorage = (localDisk = "do-storage"),
  bindings = [(name = "C", durableObjectNamespace = "Counter")],
);
`, doDir, addr)
	os.WriteFile(filepath.Join(dir, "config.capnp"), []byte(cfg), 0o600)
	os.WriteFile(filepath.Join(dir, "index.js"), []byte(counterJS), 0o600)
	var logb bytes.Buffer
	cmd := exec.Command(wd, "serve", "config.capnp", "--experimental")
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = &logb, &logb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	stop = func() {
		cmd.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			cmd.Process.Kill()
			<-done
		}
	}
	base = "http://" + addr
	for dl := time.Now().Add(30 * time.Second); ; {
		if r, err := http.Get(base + "/r?o=probe"); err == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(dl) {
			stop()
			t.Fatalf("workerd did not start:\n%s", logb.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	return base, stop
}

// TestBackupHostdWhileWriting is tw backup --hostd (on by default; issue
// #295) on a running host: real workerd keeps writing Durable Object
// storage and R2 blobs are put and deleted during the backup. Every
// database in the backup is a consistent copy (one commit), every blob is
// whole, backup.json lists them, and after more writes, tw restore puts
// the backup's DO storage back: workerd serves the backed-up values.
func TestBackupHostdWhileWriting(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	wd, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip("needs workerd (make deps): ", err)
	}
	ctx := context.Background()
	dir := t.TempDir()
	s := &state{Dir: dir, Cfg: Config{Tailnet: "t.ts.net", HostID: "h1", CreatedAt: "x"}}
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, "mgmt"), 0o700)
	st, err := mgmt.OpenStore(ctx, filepath.Join(dir, "mgmt", "mgmt.db"))
	if err != nil {
		t.Fatal(err)
	}
	doDir := filepath.Join(dir, "hostd", "tenants", "tn_a", "workloads", "app", "do")
	base, stopWD := startCounterWorkerd(t, wd, doDir)
	defer func() { stopWD() }()

	const objects = 4
	var wg sync.WaitGroup
	var stop atomic.Bool
	var writes atomic.Int64
	for i := range objects {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				r, err := http.Post(fmt.Sprintf("%s/w?o=obj-%d", base, i), "text/plain", nil)
				if err != nil {
					t.Error(err)
					return
				}
				io.Copy(io.Discard, r.Body)
				r.Body.Close()
				if r.StatusCode != 200 {
					t.Errorf("write: %s", r.Status)
					return
				}
				writes.Add(1)
			}
		}()
	}
	// R2 blobs: put and garbage-collected (deleted) while the backup runs.
	blobs := &r2.FS{Root: filepath.Join(dir, "hostd", "r2")}
	blobBody := func(i int) []byte { return bytes.Repeat([]byte{byte('a' + i%26)}, 1+(i%40)*997) }
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; !stop.Load(); i++ {
			b := blobBody(i)
			if err := blobs.Put(ctx, fmt.Sprintf("t/tn_a/w/app/b/files/%06d", i), bytes.NewReader(b), int64(len(b))); err != nil {
				t.Error(err)
				return
			}
			if i >= 8 {
				blobs.Delete(ctx, fmt.Sprintf("t/tn_a/w/app/b/files/%06d", i-8))
			}
		}
	}()
	for writes.Load() < 200 {
		time.Sleep(5 * time.Millisecond)
	}

	out := filepath.Join(t.TempDir(), "bk")
	before := writes.Load()
	if err := cmdBackup([]string{"--state-dir", dir, "--out", out, "--json"}); err != nil {
		t.Fatal(err)
	}
	if during := writes.Load() - before; during == 0 {
		t.Fatal("no DO write happened during the backup: the test doesn't test anything")
	} else {
		t.Logf("%d DO writes during the backup", during)
	}

	var m backupManifest
	b, err := os.ReadFile(filepath.Join(out, backupManifestName))
	if err != nil || json.Unmarshal(b, &m) != nil || m.Hostd == nil {
		t.Fatalf("manifest: %v %s", err, b)
	}
	if fmt.Sprint(m.Hostd.Workloads) != "[tn_a/app]" || m.Hostd.Databases < objects+1 || m.Hostd.R2Blobs == 0 {
		t.Fatalf("manifest hostd: %+v", m.Hostd)
	}
	for _, n := range m.NotIncluded {
		if strings.Contains(n, "tw down, then copy it") && strings.Contains(n, "Durable Object") {
			t.Fatalf("manifest still says DO storage is not included: %q", n)
		}
	}

	// Every object database in the backup is one commit.
	backed := map[string]int{} // object ID → c.n
	bkDO := filepath.Join(out, "hostd", "tenants", "tn_a", "workloads", "app", "do")
	filepath.WalkDir(bkDO, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(p, "-wal") || strings.HasSuffix(p, "-shm") {
			t.Errorf("backup holds %s", p)
		}
		if !strings.HasSuffix(p, ".sqlite") || d.Name() == "metadata.sqlite" {
			return nil
		}
		db, err := sql.Open("sqlite", "file:"+p+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var ok string
		if err := db.QueryRow("PRAGMA integrity_check").Scan(&ok); err != nil || ok != "ok" {
			t.Fatalf("%s: integrity_check %q %v", p, ok, err)
		}
		var c, rows, maxN int
		if err := db.QueryRow("SELECT (SELECT n FROM c), (SELECT count(*) FROM log), (SELECT coalesce(max(n), 0) FROM log)").Scan(&c, &rows, &maxN); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if c != rows || c != maxN {
			t.Fatalf("%s is not one commit: c.n %d, %d log rows, max %d", p, c, rows, maxN)
		}
		backed[strings.TrimSuffix(d.Name(), ".sqlite")] = c
		return nil
	})
	if len(backed) < objects {
		t.Fatalf("backup holds %d object databases (want ≥ %d): %v", len(backed), objects, backed)
	}

	// Every blob in the backup is whole (no unfinished put either).
	bkR2 := filepath.Join(out, "hostd", "r2")
	nb := 0
	filepath.WalkDir(bkR2, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") {
			t.Errorf("backup holds an unfinished blob %s", p)
			return nil
		}
		i, _ := strconv.Atoi(d.Name())
		if got, _ := os.ReadFile(p); !bytes.Equal(got, blobBody(i)) {
			t.Errorf("blob %s: %d bytes, not the %d put", p, len(got), len(blobBody(i)))
		}
		nb++
		return nil
	})
	if nb == 0 || nb != m.Hostd.R2Blobs {
		t.Fatalf("backup holds %d blobs, manifest says %d", nb, m.Hostd.R2Blobs)
	}

	// More writes after the backup, then tw down and tw restore.
	for writes.Load() < before+400 {
		time.Sleep(5 * time.Millisecond)
	}
	stop.Store(true)
	wg.Wait()
	stopWD()
	st.Close()
	if err := cmdRestore([]string{out, "--state-dir", dir, "--yes", "--json"}); err != nil {
		t.Fatal(err)
	}
	base, stopWD = startCounterWorkerd(t, wd, doDir)
	for i := range objects {
		r, err := http.Get(fmt.Sprintf("%s/r?o=obj-%d", base, i))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		f := strings.Fields(string(body))
		if len(f) != 2 {
			t.Fatalf("read obj-%d: %s %s", i, r.Status, body)
		}
		n, _ := strconv.Atoi(f[1])
		if want, ok := backed[f[0]]; !ok || n != want {
			t.Fatalf("obj-%d (%s) after the restore: n = %d, want the backup's %d (%v)", i, f[0], n, want, ok)
		}
	}
	// The replaced DO storage is in the safety backup, which says so.
	safety, _ := filepath.Glob(filepath.Join(dir, "backups", "*-before-restore"))
	if len(safety) != 1 {
		t.Fatalf("safety backups: %v", safety)
	}
	var sm backupManifest
	if b, err := os.ReadFile(filepath.Join(safety[0], backupManifestName)); err != nil || json.Unmarshal(b, &sm) != nil || sm.Hostd == nil || fmt.Sprint(sm.Hostd.Workloads) != "[tn_a/app]" {
		t.Fatalf("safety manifest: %v %s", err, b)
	}
	if !fileExists(filepath.Join(safety[0], "hostd", "tenants", "tn_a", "workloads", "app", "do")) || !fileExists(filepath.Join(safety[0], "hostd", "r2")) {
		t.Fatal("the safety backup doesn't hold the replaced DO storage and blobs")
	}
}

// TestBackupHostdOff is tw backup --hostd=false: the control plane only,
// and tw restore of it leaves hostd/ as it is.
func TestBackupHostdOff(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := &state{Dir: dir, Cfg: Config{Tailnet: "t.ts.net", HostID: "h1", CreatedAt: "x"}}
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, "mgmt"), 0o700)
	st, err := mgmt.OpenStore(ctx, filepath.Join(dir, "mgmt", "mgmt.db"))
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	live := filepath.Join(dir, "hostd", "tenants", "tn_a", "workloads", "app", "do", "x.facets")
	os.MkdirAll(filepath.Dir(live), 0o700)
	os.WriteFile(live, []byte("v1"), 0o600)
	if err := cmdBackup([]string{"--state-dir", dir, "--out", filepath.Join(dir, "hostd", "bk"), "--json"}); err == nil {
		t.Fatal("a backup inside hostd/ was not refused")
	}
	out := filepath.Join(t.TempDir(), "bk")
	if err := cmdBackup([]string{"--state-dir", dir, "--out", out, "--hostd=false", "--json"}); err != nil {
		t.Fatal(err)
	}
	var m backupManifest
	b, _ := os.ReadFile(filepath.Join(out, backupManifestName))
	if json.Unmarshal(b, &m) != nil || m.Hostd != nil || fileExists(filepath.Join(out, "hostd")) {
		t.Fatalf("--hostd=false backup: %s", b)
	}
	if !strings.Contains(strings.Join(m.NotIncluded, "\n"), filepath.Join(dir, "hostd")+" (Durable Object storage") {
		t.Fatalf("not included: %v", m.NotIncluded)
	}
	os.WriteFile(live, []byte("v2"), 0o600)
	if err := cmdRestore([]string{out, "--state-dir", dir, "--yes", "--json"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(live); string(b) != "v2" {
		t.Fatalf("restore of a --hostd=false backup changed DO storage: %q", b)
	}
	var names []string
	ents, _ := os.ReadDir(filepath.Join(dir, "hostd", "tenants", "tn_a", "workloads"))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if fmt.Sprint(names) != "[app]" {
		t.Fatalf("workloads after restore: %v", names)
	}
}
