//go:build linux

package helper

import "github.com/rosskukulinski/tailworks/hostd/vm/helperapi"

// The snapshot-base guard (security ledger H16). Only a VM started with
// StartRequest.Base may become a base snapshot, and such a VM never gets a
// volume. The helper enforces this itself rather than trusting hostd to
// snapshot only a fresh, unloaded base boot: before, it checked only for a
// free volume slot, which a claimed tenant VM without a DO volume still
// had. Every copy restored from a base shares the base's memory, so a base
// must never have held a workload.
//
// The helper can't see what hostd sends a guest over vsock (a bundle, a
// MAC key), only the volume it attaches. hostd's checks (a fresh boot, an
// empty fsscan) stay the main guard; this one makes a hostd bug fail closed.

// checkBaseStart validates StartRequest.Base: a base is a pool VM (a volume
// slot, no image) booted fresh, not restored.
func checkBaseStart(req *helperapi.StartRequest) error {
	if req.Base && (!req.VolumeSlot || req.Image || req.Restore != "") {
		return herr(helperapi.CodeBadRequest, "a base VM is a fresh pool VM (volume slot, no image, not restored)")
	}
	return nil
}

// canSnapshotBase: only a session started with Base that holds no volume.
func (sess *session) canSnapshotBase() error {
	if !sess.forBase || !sess.slot2 || sess.vol != nil || sess.base != nil {
		return herr(helperapi.CodeBadRequest, "only a VM started as a base (a fresh pool VM, no volume) can become a base snapshot")
	}
	return nil
}

// canAttach: a free volume slot, on a session not started as a base.
func (sess *session) canAttach() error {
	if sess.forBase {
		return herr(helperapi.CodeBadRequest, "a VM started as a base never gets a volume")
	}
	if !sess.slot2 || sess.vol != nil {
		return herr(helperapi.CodeBadRequest, "this VM has no free volume slot")
	}
	return nil
}
