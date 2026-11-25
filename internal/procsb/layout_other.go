//go:build !linux

package procsb

// ReadableTrees is empty off Linux: Seatbelt's deny rules win inside an
// allowed directory, so the layout check has nothing to refuse.
func ReadableTrees() []string { return nil }
