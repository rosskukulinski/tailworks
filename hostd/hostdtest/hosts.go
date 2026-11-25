package hostdtest

import (
	"context"
	"path/filepath"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// Restart stops e's tw-hostd and starts it again on the same data and run
// directories, as a hostd restart (or upgrade) does. Its workloads come
// back from mgmt's assignments.
func (e *Env) Restart() {
	e.T.Helper()
	e.RestartAfter(nil)
}

// RestartAfter is Restart, running between (if set) while hostd is down.
func (e *Env) RestartAfter(between func()) {
	e.T.Helper()
	e.Host.Close()
	if between != nil {
		between()
	}
	e.Host = e.startHost(e.cfg)
}

// DataDir is the data directory of e's tw-hostd.
func (e *Env) DataDir() string { return e.cfg.DataDir }

// StartHost starts a second tw-hostd, id, on e's fakemgmt, with its own
// data and run directories (another machine, as far as mgmt and the stores
// can tell). Move a workload to it with e.FM.MoveWorkload.
func (e *Env) StartHost(id string) *hostd.Host {
	e.T.Helper()
	if e.RealMgmt != nil {
		e.T.Fatal("hostdtest: StartHost is fakemgmt-only")
	}
	cfg := e.cfg
	cfg.HostID = id
	cfg.DataDir = filepath.Join(e.Dir, "data-"+id)
	cfg.RunDir = filepath.Join(e.Dir, "run-"+id)
	cfg.HostnamePrefix = e.cfg.HostnamePrefix + id + "-"
	mc, err := mgmtapi.NewClient("unix://" + filepath.Join(e.Dir, "mgmt.sock"))
	if err != nil {
		e.T.Fatal(err)
	}
	cfg.Mgmt = mc
	h, err := hostd.New(cfg)
	if err != nil {
		e.T.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		e.T.Fatal(err)
	}
	e.T.Cleanup(func() { h.Close() })
	return h
}
