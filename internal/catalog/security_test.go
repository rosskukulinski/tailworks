package catalog

import "testing"

// TestHasSecurityNotes: what Markdown renders for a security fragment is
// what HasSecurityNotes recognizes, and nothing less.
func TestHasSecurityNotes(t *testing.T) {
	t.Parallel()
	sec := Note{Kind: KindSecurity, Source: "fix-1.md", Text: "A V8 fix."}
	fix := Note{Kind: KindFix, Source: "fix-2.md", Text: "Mentions security in passing. ## Security is not a heading here."}
	if !HasSecurityNotes(Markdown("tw v0.6.1", []Note{sec, fix})) {
		t.Error("a release with a security note is not recognized")
	}
	if HasSecurityNotes(Markdown("tw v0.6.0", []Note{fix})) {
		t.Error("a release without a security note is recognized")
	}
	// release.yml appends an install line; CRLF bodies still count.
	if !HasSecurityNotes("# tw v0.6.1\r\n\r\n## Security\r\n\r\n### x\r\n\r\ntext\r\n\r\nInstall: see docs/INSTALL.md\r\n") {
		t.Error("CRLF body not recognized")
	}
	for _, body := range []string{"", "## Security notes\n", "### Security\n", "Security\n", "text ## Security\n"} {
		if HasSecurityNotes(body) {
			t.Errorf("%q recognized as a security release", body)
		}
	}
}
