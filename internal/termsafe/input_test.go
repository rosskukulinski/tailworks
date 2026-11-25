package termsafe

import "testing"

func TestCheckName(t *testing.T) {
	for _, s := range []string{"", "lobby", "room 1", "a/b", `C:\x1b`, "café", "部屋", "עברית", "🎉"} {
		if err := CheckName(s); err != nil {
			t.Errorf("CheckName(%q) = %v", s, err)
		}
	}
	for _, s := range []string{"a\x00", "a\x1b[31m", "a\r\nb", "a\tb", "a\x7f", "a\u0085", "a\u009b", "a\u202e", "a\u2066", "a\u200f", "a\u2028", "a\xff"} {
		if err := CheckName(s); err == nil {
			t.Errorf("CheckName(%q) accepted", s)
		}
	}
}
