package termsafe

import (
	"fmt"
	"unicode/utf8"
)

// CheckName refuses a name that String would have to escape: a C0
// control (TAB included), DEL, a C1 control, a bidi control or mark, a
// Unicode line or paragraph separator, or invalid UTF-8. It is the input
// half of ledger F9 (#185), used where a new object name enters the
// platform (hostd ensure/admit, mgmt CreateObject); names that already
// exist are not re-checked and still render through String.
func CheckName(s string) error {
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			return fmt.Errorf("invalid UTF-8 at byte %d", i)
		case r < 0x20 || r == 0x7f || unsafe(r):
			return fmt.Errorf("control character U+%04X at byte %d (no C0, DEL, C1 or bidi controls)", r, i)
		}
		i += n
	}
	return nil
}
