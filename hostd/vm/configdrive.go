package vm

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/rosskukulinski/tailworks/guest/proto"
	"github.com/rosskukulinski/tailworks/hostd/sandbox"
)

// The guest network is the same in every VM (each has its own host-side
// stack, so addresses never collide).
var (
	gatewayIP = netip.MustParseAddr(proto.GatewayIP)
	guestIP   = netip.MustParseAddr(proto.GuestIP)
	gwMAC     = net.HardwareAddr{0x06, 0x00, 0xc0, 0xa8, 0x48, 0x01}
)

const guestMAC = "06:00:c0:a8:48:02"

// ---- config drive ----

// writeConfigDrive writes the config drive: guest.json + the instance dir,
// as a raw tar padded to 4 KiB. token is the boot's control-channel token
// (agent.ctlToken); extra adjusts guest.json (backend-specific fields)
// before it is written.
func writeConfigDrive(path string, s sandbox.Spec, mtu int, token string, extra func(*proto.GuestConfig)) error {
	gc := proto.GuestConfig{UnixNano: time.Now().UnixNano(), IP: proto.GuestIP, Gateway: proto.GatewayIP, Prefix: proto.PrefixLen,
		MTU: mtu, WorkerdArgs: s.WorkerdArgs, HealthCF: s.HealthCF, Debug: s.Debug, CtlToken: token}
	if s.Volume != nil {
		gc.Volume = proto.VolumeDev
	}
	if extra != nil {
		extra(&gc)
	}
	return writeDrive(path, gc, func(tw *tar.Writer) error { return tarDir(tw, s.AppDir) })
}

// The warm-up app on a pool VM's config drive: tailworks' own, no tenant code.
// One request through a Durable Object with SQLite and KV storage faults in
// the code paths the platform runtime uses (workerd, V8, the isolate, SQLite),
// on top of the workerd binary itself. It listens on loopback only.
const (
	warmupCapnp = `using Workerd = import "/workerd/workerd.capnp";
const config :Workerd.Config = (
  services = [ (name = "main", worker = .w), (name = "do", disk = (path = "/run/tailworks/app/warmdo", writable = true)) ],
  sockets = [ (name = "in", address = "127.0.0.1:8080", service = "main", http = (cfBlobHeader = "x-tw-cf")) ],
);
const w :Workerd.Worker = (
  compatibilityDate = "2026-04-01",
  modules = [ (name = "w.js", esModule = embed "w.js") ],
  durableObjectNamespaces = [ (className = "W", uniqueKey = "tw-warmup", enableSql = true) ],
  durableObjectStorage = (localDisk = "do"),
  bindings = [ (name = "NS", durableObjectNamespace = "W") ],
);
`
	warmupJS = `import { DurableObject } from "cloudflare:workers";
export class W extends DurableObject {
  async fetch(req) {
    const sql = this.ctx.storage.sql;
    sql.exec("CREATE TABLE IF NOT EXISTS t (k TEXT PRIMARY KEY, v TEXT)");
    sql.exec("INSERT OR REPLACE INTO t VALUES (?, ?)", "k", "v");
    await this.ctx.storage.put("k", { n: 1 });
    const v = await this.ctx.storage.get("k");
    return new Response("warm " + sql.exec("SELECT count(*) AS n FROM t").one().n + " " + JSON.stringify(v));
  }
}
export default {
  async fetch(req, env) {
    const r = await env.NS.get(env.NS.idFromName("w")).fetch("http://w/");
    return new Response(await r.text() + " " + JSON.stringify(req.cf ?? {}));
  }
};
`
)

// writePoolDrive writes a pool VM's config drive: the network config, its
// pre-claim control-channel token and the warm-up app (plus
// proto.WarmupMarker, which fsscan must not find after the warm-up). No
// tenant code, no keys.
func writePoolDrive(path string, mtu int, token string) error {
	gc := proto.GuestConfig{UnixNano: time.Now().UnixNano(), IP: proto.GuestIP, Gateway: proto.GatewayIP, Prefix: proto.PrefixLen,
		MTU: mtu, Pool: true, WarmupArgs: []string{"serve", "config.capnp", "--experimental"}, WarmupHealthCF: "{}", CtlToken: token}
	return writeDrive(path, gc, func(tw *tar.Writer) error {
		if err := tw.WriteHeader(&tar.Header{Name: "warmdo/", Mode: 0o700, Typeflag: tar.TypeDir}); err != nil {
			return err
		}
		for name, body := range map[string]string{"config.capnp": warmupCapnp, "w.js": warmupJS, proto.WarmupMarker: "tw warm-up app (no tenant code)\n"} {
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
				return err
			}
			if _, err := io.WriteString(tw, body); err != nil {
				return err
			}
		}
		return nil
	})
}

// bundleTar is the workload's instance dir as a tar (a pool VM's load).
func bundleTar(dir string) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tarDir(tw, dir); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeDrive(path string, gc proto.GuestConfig, body func(*tar.Writer) error) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	gb, _ := json.Marshal(gc)
	if err := tw.WriteHeader(&tar.Header{Name: proto.GuestConfigFile, Mode: 0o600, Size: int64(len(gb)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	tw.Write(gb)
	if err := body(tw); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if pad := buf.Len() % 4096; pad != 0 {
		buf.Write(make([]byte, 4096-pad))
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}

func tarDir(tw *tar.Writer, root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			return tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(rel) + "/", Mode: 0o700, Typeflag: tar.TypeDir})
		}
		if !d.Type().IsRegular() {
			return nil // sockets etc.
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		mode := int64(0o600)
		if fi, err := d.Info(); err == nil && fi.Mode()&0o100 != 0 {
			mode = 0o700 // keep the owner exec bit (platform test binaries)
		}
		if err := tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(rel), Mode: mode, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = tw.Write(b)
		return err
	})
}
