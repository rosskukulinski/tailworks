//go:build linux

package procsb

// ReadableTrees are the directories every Landlock profile allows reading
// and executing below (systemRO), whether or not they exist on this host.
// Nothing of tailworks' may live under them: see CheckLayout.
func ReadableTrees() []string { return append([]string(nil), systemRO...) }

// checkLayout refuses a profile whose Deny list Landlock could not honour:
// a denied path that lies under a tree the rules allow (systemRO, or the
// profile's own ReadOnly trees). It runs in the launcher before the rules
// are applied, so a workload never starts under a profile that would let
// it read what hostd meant to deny (issue #342). hostd and tw check the
// same thing earlier, with the path's role in the message.
func (p *Profile) checkLayout() error {
	var paths []LayoutPath
	for _, d := range p.Deny {
		paths = append(paths, LayoutPath{What: "denied path", Path: d})
	}
	return CheckLayout(paths, append(ReadableTrees(), p.ReadOnly...))
}
