package procsb

import (
	"fmt"
	"strings"
	"testing"
)

// On Landlock ABI 1 (Ubuntu 22.04's 5.15) a rule asking for "refer" makes
// go-landlock's BestEffort mode drop the whole ruleset, so the own-dirs
// rule must not ask for it there. make e2e-linux (ubuntu22-arm64) caught
// `tw doctor`: "Landlock did not enforce".
func TestOwnRuleReferOnlyFromABI2(t *testing.T) {
	for abi, want := range map[int]bool{1: false, 2: true, 4: true} {
		got := strings.Contains(fmt.Sprint(ownRule([]string{"/tmp/x"}, abi)), "refer")
		if got != want {
			t.Errorf("ABI %d: refer requested = %v, want %v (%v)", abi, got, want, ownRule([]string{"/tmp/x"}, abi))
		}
	}
}
