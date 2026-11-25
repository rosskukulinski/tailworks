// Package mgmtpg is the shared Postgres side of the hermetic multi-replica
// tw-mgmt harnesses (e2e/multimgmt and e2e/dataplane's TW_DP_MGMT=pg3): a
// throwaway Postgres container and a fake setec custody the replicas share
// (the fault proxy between each replica and Postgres is e2e/chaoskit's). It is
// test support only; nothing here touches a real tailnet.
package mgmtpg

import (
	"database/sql"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // the pgx driver, for the readiness ping
)

// Password is the throwaway container's postgres password.
const Password = "twmm"

// Options says where the container runs.
type Options struct {
	// Name is the container's name. Concurrent runs (other worktrees, other
	// suites) need distinct names: starting removes any container of that
	// name first.
	Name string
	// Port is the 127.0.0.1 port to publish; "" picks a free one.
	Port string
	// Ctx is the docker context ("" is "orbstack"; CI uses "default").
	Ctx string
	// LogDir, when set, receives postgres.log when the test ends.
	LogDir string
	// Record, when set, is told about the container (kind "container").
	Record func(kind, name string, pid int)
}

// Postgres is a running throwaway container.
type Postgres struct {
	DSN  string
	name string
	ctx  string
}

func (p *Postgres) docker(args ...string) ([]byte, error) {
	return exec.Command("docker", append([]string{"--context", p.ctx}, args...)...).CombinedOutput()
}

// Start runs a fresh postgres:17-alpine container and waits until it
// answers. It is removed (its log kept in LogDir) when the test ends.
func Start(t testing.TB, o Options) *Postgres {
	t.Helper()
	if o.Ctx == "" {
		o.Ctx = "orbstack"
	}
	if o.Port == "" {
		o.Port = freePort(t)
	}
	p := &Postgres{name: o.Name, ctx: o.Ctx}
	p.docker("rm", "-f", o.Name)
	out, err := p.docker("run", "-d", "--rm", "--name", o.Name, "-e", "POSTGRES_PASSWORD="+Password,
		"-p", "127.0.0.1:"+o.Port+":5432", "postgres:17-alpine",
		"-c", "log_lock_waits=on", "-c", "deadlock_timeout=200ms", "-c", "track_activity_query_size=8192",
		"-c", "max_connections=300")
	if err != nil {
		t.Fatalf("docker run postgres: %v: %s", err, out)
	}
	if o.Record != nil {
		o.Record("container", o.Name, 0)
	}
	t.Cleanup(func() {
		if o.LogDir != "" {
			if logs, err := p.docker("logs", o.Name); err == nil {
				os.WriteFile(filepath.Join(o.LogDir, "postgres.log"), logs, 0o600)
			}
		}
		p.docker("rm", "-f", o.Name)
	})
	p.DSN = "postgres://postgres:" + Password + "@127.0.0.1:" + o.Port + "/postgres?sslmode=disable"
	if err := WaitReady(p.DSN, 60*time.Second); err != nil {
		t.Fatalf("postgres not ready: %v", err)
	}
	return p
}

// Restart restarts the container in place (its data is kept).
func (p *Postgres) Restart() ([]byte, error) {
	return p.docker("restart", "-t", "2", p.name)
}

// Dump is pg_dump of the database, run in the container (a test can check
// that no secret value reached the store).
func (p *Postgres) Dump() ([]byte, error) {
	return exec.Command("docker", "--context", p.ctx, "exec", p.name, "pg_dump", "-U", "postgres", "postgres").Output()
}

// WaitReady pings dsn until it answers or timeout passes.
func WaitReady(dsn string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		db, err := sql.Open("pgx", dsn)
		if err == nil {
			err = db.Ping()
			db.Close()
		}
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// ViaProxy rewrites a postgres:// DSN to go through addr (a chaoskit.Proxy).
func ViaProxy(t testing.TB, dsn, addr string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Host = addr
	return u.String()
}

// Host is dsn's host:port (a proxy's target).
func Host(t testing.TB, dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

func freePort(t testing.TB) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}
