// Package termsafe makes strings that a tenant or the internet controls
// safe to print on an operator's terminal (security/LOOP.md invariant I5,
// ledger H13).
//
// A public request path, an object name, a tenant's audit note or a line
// of tenant console output can carry ESC (recolour, clear or retitle the
// terminal, OSC 8 links), CR and LF (forge or overwrite a `tw audit` row),
// NUL, DEL, C1 controls (U+009B is CSI on some terminals), raw invalid
// UTF-8 bytes (0x9b is CSI in 8-bit mode) and bidi overrides (U+202E
// reorders what the operator reads). String renders each of these visibly
// (`\x1b`, `\n`, `\u202e`, …) and leaves every other rune as it is.
//
// Use it on human-readable output only. --json output is encoded by
// encoding/json, which already escapes C0 (ESC, CR, LF, NUL as \u00XX)
// and U+2028/U+2029 and replaces invalid UTF-8; JSON escapes what JSON
// leaves raw (DEL, C1, bidi) without changing a decoded value.
package termsafe

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// String returns s with every control, bidi and line-separator character
// escaped, so it prints as one line that cannot move the cursor or change
// the terminal's state. Backslash is left alone: the result is for
// people, not for parsing.
func String(s string) string { return escape(s, false) }

// Line is String for one line of log output: TAB is kept, since stack
// traces indent with it and it cannot forge a line.
func Line(s string) string { return escape(s, true) }

// Text is Line for multi-line text a person reads (an error message, a
// bundler's output): LF and TAB are kept, a CR before LF is dropped, and
// everything else String escapes is escaped.
func Text(s string) string {
	if clean(strings.ReplaceAll(s, "\n", ""), true) {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = Line(strings.TrimSuffix(l, "\r"))
	}
	return strings.Join(lines, "\n")
}

// Strings applies String to each element (tag lists and the like).
func Strings(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = String(s)
	}
	return out
}

func escape(s string, keepTab bool) string {
	if clean(s, keepTab) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			b.WriteString(`\x`)
			b.WriteString(hex2(s[i]))
		case r == '\t' && keepTab:
			b.WriteByte('\t')
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			b.WriteString(`\x`)
			b.WriteString(hex2(byte(r)))
		case unsafe(r):
			b.WriteString(`\u`)
			b.WriteString(hex4(r))
		default:
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	return b.String()
}

// clean is the fast path: most strings need no escaping.
func clean(s string, keepTab bool) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x80 {
			return cleanUTF8(s[i:])
		}
		if (c < 0x20 && !(keepTab && c == '\t')) || c == 0x7f {
			return false
		}
	}
	return true
}

func cleanUTF8(s string) bool {
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && n == 1) || (r < 0x20 && r != '\t') || r == 0x7f || unsafe(r) {
			return false
		}
		i += n
	}
	return true
}

// unsafe reports the non-ASCII runes String escapes: C1 controls, the
// bidi embedding/override/isolate controls and marks, and the Unicode
// line and paragraph separators.
func unsafe(r rune) bool {
	switch {
	case r >= 0x80 && r <= 0x9f: // C1 (U+009B CSI, U+009D OSC, U+0085 NEL)
		return true
	case r == 0x061c, // ARABIC LETTER MARK
		r == 0x200e, r == 0x200f, // LRM, RLM
		r >= 0x202a && r <= 0x202e, // LRE RLE PDF LRO RLO
		r >= 0x2066 && r <= 0x2069, // LRI RLI FSI PDI
		r == 0x2028, r == 0x2029:   // LINE / PARAGRAPH SEPARATOR
		return true
	}
	return false
}

// JSON rewrites encoded JSON so the runes encoding/json leaves raw (DEL,
// C1, bidi controls) appear as \u escapes. Those runes can only occur
// inside JSON strings, where \uXXXX decodes to the same rune, so the
// decoded value is unchanged. Every other byte is copied as is.
func JSON(b []byte) []byte {
	out := make([]byte, 0, len(b)+16)
	for i := 0; i < len(b); {
		r, n := utf8.DecodeRune(b[i:])
		if r == 0x7f || (unsafe(r) && n > 1) {
			out = append(out, `\u`...)
			out = append(out, hex4(r)...)
		} else {
			out = append(out, b[i:i+n]...)
		}
		i += n
	}
	return out
}

func hex2(c byte) string {
	s := strconv.FormatUint(uint64(c), 16)
	if len(s) < 2 {
		s = "0" + s
	}
	return s
}

func hex4(r rune) string {
	s := strconv.FormatUint(uint64(r), 16)
	return strings.Repeat("0", 4-len(s)) + s
}
