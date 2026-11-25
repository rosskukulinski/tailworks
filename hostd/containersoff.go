package hostd

// containersOff is why this host offers no containers, for tw doctor and
// tw up (issue #496): the reason tw-hostd found at startup
// (Config.ContainersOff: no helper support, a guest kernel without erofs,
// no mkfs.erofs, -no-containers), else what the config says. "" when it
// offers them.
func (h *Host) containersOff() string {
	switch {
	case h.containersOffered():
		return ""
	case h.cfg.VM == nil || h.cfg.ForceProcessTier:
		return "needs the microVM tier"
	case h.cfg.ContainersOff != "":
		return h.cfg.ContainersOff
	}
	return "no container backend"
}
