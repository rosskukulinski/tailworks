package termsafe

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// hostile carries every class String must escape.
const hostile = "a\x1b[31mRED\x1b]0;title\x07\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\" +
	"\r\nFORGED\x00nul\x7fdel\x08bs\ttab\u009bcsi\u0085nel\u202eevil\u2066iso\u2069\u200f\u2028sep\xffraw\x9b"

func TestStringEscapesEverything(t *testing.T) {
	got := String(hostile)
	for i := 0; i < len(got); i++ {
		if c := got[i]; c < 0x20 || c == 0x7f {
			t.Fatalf("raw control byte %#x at %d in %q", c, i, got)
		}
	}
	if !utf8.ValidString(got) {
		t.Fatalf("invalid UTF-8 left in %q", got)
	}
	for _, r := range got {
		if unsafe(r) {
			t.Fatalf("unsafe rune %U left in %q", r, got)
		}
	}
	for _, want := range []string{`\x1b[31mRED`, `\x07`, `\r\nFORGED`, `\x00nul`, `\x7fdel`, `\x08bs`, `\ttab`, `\u009bcsi`, `\u0085nel`, `\u202eevil`, `\u2066iso`, `\u2069`, `\u200f`, `\u2028sep`, `\xffraw`, `\x9b`} {
		if !strings.Contains(got, want) {
			t.Errorf("String(hostile) = %q, want it to contain %q", got, want)
		}
	}
}

func TestStringLeavesOrdinaryTextAlone(t *testing.T) {
	for _, s := range []string{"", "plain", "GET /webhooks/a%1b?x=1", `C:\path\x1b`, "héllo → 世界 🎉", "עברית"} {
		if got := String(s); got != s {
			t.Errorf("String(%q) = %q", s, got)
		}
	}
}

func TestLineKeepsTab(t *testing.T) {
	if got := Line("\tat main.go:1\x1b[2J\n"); got != "\tat main.go:1\\x1b[2J\\n" {
		t.Fatalf("Line = %q", got)
	}
}

func TestStrings(t *testing.T) {
	if got := Strings([]string{"tag:a", "tag:\x1bb"}); !reflect.DeepEqual(got, []string{"tag:a", `tag:\x1bb`}) {
		t.Fatalf("Strings = %q", got)
	}
}

// encoding/json escapes C0 (ESC, CR, LF, NUL) itself but leaves DEL, C1
// and bidi raw; JSON escapes those without changing the decoded value.
func TestJSONEscapesWhatEncodingJSONLeavesRaw(t *testing.T) {
	in := map[string]any{"path": hostile, "k\u202e": []any{"x\u009by", 1.5, true}}
	b, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []byte{0x1b, 0x00, '\r', 0x07} {
		if strings.IndexByte(string(b), c) >= 0 {
			t.Fatalf("encoding/json left raw %#x: %q", c, b)
		}
	}
	if !strings.Contains(string(b), "\u202e") || !strings.Contains(string(b), "\x7f") {
		t.Fatalf("expected encoding/json to leave bidi and DEL raw (premise of JSON): %q", b)
	}
	out := JSON(b)
	for i := 0; i < len(out); i++ {
		if c := out[i]; (c < 0x20 && c != '\n') || c == 0x7f {
			t.Fatalf("raw %#x at %d in %q", c, i, out)
		}
	}
	for _, r := range string(out) {
		if unsafe(r) {
			t.Fatalf("unsafe rune %U left in %q", r, out)
		}
	}
	var a, z any
	if err := json.Unmarshal(b, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &z); err != nil {
		t.Fatalf("JSON output does not parse: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(a, z) {
		t.Fatalf("decoded value changed:\n%#v\n%#v", a, z)
	}
	plain := []byte(`{"a": "b → c"}`)
	if got := JSON(plain); string(got) != string(plain) {
		t.Fatalf("JSON(plain) = %q", got)
	}
}

func TestText(t *testing.T) {
	if got, want := Text("line 1\r\n\tline 2\x1b[1A\x1b[2K\u202e"), "line 1\n\tline 2\\x1b[1A\\x1b[2K\\u202e"; got != want {
		t.Fatalf("Text = %q, want %q", got, want)
	}
	if s := "a\nb\tc"; Text(s) != s {
		t.Fatalf("Text(%q) = %q", s, Text(s))
	}
}

func TestWriter(t *testing.T) {
	var b strings.Builder
	w := NewWriter(&b)
	for _, chunk := range []string{"ok\tline\r\n", "\x1b[2J", "cleared\nhalf\u202e", "\xe2\x80", "\xaeend"} {
		w.Write([]byte(chunk))
	}
	if got, want := b.String(), "ok\tline\n\\x1b[2Jcleared\n"; got != want {
		t.Fatalf("before Flush: %q, want %q", got, want)
	}
	w.Flush()
	// U+202E split across two writes is still recognised.
	if got, want := b.String(), "ok\tline\n\\x1b[2Jcleared\nhalf\\u202e\\u202eend\n"; got != want {
		t.Fatalf("after Flush: %q, want %q", got, want)
	}
}
