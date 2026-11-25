package netd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/health"
	"tailscale.com/ipn"
)

// A ready node that control rejects (issue #200).
//
// up() catches a rejected rejoin only until tsnet.Up returns. A node with
// tailscale's netmap cache (profile-data/<profile>/netmap-cache, on when
// control sends the cache-network-maps nodeAttr) goes Running from the
// cached netmap before control has answered, so it is Ready first and
// control's NeedsLogin arrives after. Control can also reject a node that
// has been up for a while (an admin removed the device, its key expired).
// Either way the node is dead: peers no longer see it.
//
// So every ready node keeps watching its IPN bus (watchRejected). When
// NeedsLogin, or a NodeKeyExpired login error, persists for needsLoginGrace,
// rejectReady takes the node out of service (state failed, flows reset,
// node closed), drops its node state, leaves a remint mark, and calls
// Config.OnRejected. The owner restarts the object with Create; that join
// finds the mark and asks for its key with KeyRequest.Remint, as up()'s
// own rejection path does, so it rejoins under the same hostname.
//
// The alternative, holding Ready on a rejoin until a live (not cached) map
// response, was rejected: it covers only the first moment, and it would
// take away what the cache is for, a node that serves its peers while
// control is unreachable.
//
// Kill and Close win: rejectReady acts only on a node still in StateReady,
// checked and changed under o.mu, and both set their own state first.

// errNodeRejected: control rejected a node after it was ready.
var errNodeRejected = errors.New("node rejected by control after it was ready (NeedsLogin or an expired node key)")

// remintMark is left in an object's dir when its node was rejected after
// ready, so the next join asks for a re-mint (it survives a restart).
const remintMark = "remint"

// watchRejected watches a ready node until ctx ends or the node leaves
// StateReady, and calls rejectReady once control has rejected it.
func (o *Object) watchRejected(ctx context.Context, lc *local.Client) {
	for ctx.Err() == nil && o.State() == StateReady {
		if o.watchRejectedOnce(ctx, lc) {
			o.rejectReady(lc)
			return
		}
		// The bus watch ended (tailscaled drops a watcher that falls
		// behind): watch again.
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

// watchRejectedOnce runs one IPN bus watch. It returns true once NeedsLogin
// or an expired node key has lasted needsLoginGrace with no Starting or
// Running in between.
func (o *Object) watchRejectedOnce(ctx context.Context, lc *local.Client) bool {
	w, err := lc.WatchIPNBus(ctx, ipn.NotifyInitialState|ipn.NotifyInitialHealthState)
	if err != nil {
		return false
	}
	defer w.Close()
	fired := make(chan struct{})
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	needsLogin, keyExpired := false, false
	for {
		n, err := w.Next()
		if err != nil {
			select {
			case <-fired:
				return true
			default:
				return false
			}
		}
		if n.Health != nil {
			keyExpired = nodeKeyExpired(n.Health)
		}
		if n.State != nil {
			switch *n.State {
			case ipn.NeedsLogin:
				needsLogin = true
			case ipn.Starting, ipn.Running:
				needsLogin, keyExpired = false, false
			}
		}
		switch {
		case (needsLogin || keyExpired) && timer == nil:
			timer = time.AfterFunc(needsLoginGrace, func() {
				close(fired)
				w.Close() // ends Next
			})
		case !needsLogin && !keyExpired && timer != nil:
			if timer.Stop() {
				timer = nil
			}
		}
	}
}

// nodeKeyExpired reports whether the health state carries control's
// NodeKeyExpired login error (loginRejectedNotify's health form).
func nodeKeyExpired(hs *health.State) bool {
	return loginRejectedNotify(&ipn.Notify{Health: hs})
}

// rejectReady takes a ready node that control rejected out of service and
// tells the owner (Config.OnRejected). It does nothing if the node is no
// longer the ready node lc belongs to (killed, closed, or already failed).
func (o *Object) rejectReady(lc *local.Client) {
	r := o.reg
	o.mu.Lock()
	if o.state != StateReady || o.lc != lc {
		o.mu.Unlock()
		return
	}
	o.state, o.err = StateFailed, errNodeRejected
	hostname, node := o.hostname, o.nodeID
	o.mu.Unlock()
	r.log.Warn("node rejected by control after ready; closing it so it can be re-minted under the same hostname",
		"object", o.id, "hostname", hostname, "node", string(node))
	o.abortFlows()
	o.teardown()
	// Drop the node state (object.json and certs stay), as dropSavedState
	// does, and mark the dir so the next join asks for a re-mint.
	os.Remove(filepath.Join(o.dir, "tailscaled.state"))
	if err := os.WriteFile(filepath.Join(o.dir, remintMark), nil, 0o600); err != nil {
		r.log.Warn("remint mark not written", "object", o.id, "err", err)
	}
	if f := r.cfg.OnRejected; f != nil {
		f(o.id)
	}
}

// hasRemintMark reports whether dir has a remint mark. start clears it
// (clearRemintMark) once the re-minted node is up, so a join that fails
// before that asks for a re-mint again.
func hasRemintMark(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, remintMark))
	return err == nil
}

func clearRemintMark(dir string) { os.Remove(filepath.Join(dir, remintMark)) }
