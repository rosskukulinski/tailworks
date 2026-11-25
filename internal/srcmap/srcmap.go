// Package srcmap reads source maps (revision 3, as esbuild writes them)
// and maps V8 stack traces from a bundle back to the app's sources: the
// `dist/index.js:129:54` of an uncaught exception becomes
// `src/index.ts:5:11`, as `wrangler dev` shows it.
//
// A map comes from the tenant's bundle, so Parse bounds its work: one
// linear pass over "mappings" that keeps only a checkpoint per generated
// line (the running source state), and Lookup decodes the one line asked
// for. A malformed map is an error, never a panic.
package srcmap

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MaxMapBytes bounds a map Parse accepts.
const MaxMapBytes = 32 << 20

// Map is a parsed source map.
type Map struct {
	Sources []string
	Names   []string

	mappings string
	// lines[i] is where generated line i starts in mappings, and the
	// running source state there (source, line, column and name are
	// deltas across lines; only the generated column resets per line).
	lines []checkpoint
}

type checkpoint struct {
	off                    int
	src, line, col, nameIx int32
}

// Pos is a source position: Line and Column are 1-based, as in a stack trace.
type Pos struct {
	Source string
	Line   int
	Column int
	Name   string
}

// Parse reads a source map.
func Parse(b []byte) (*Map, error) {
	if len(b) > MaxMapBytes {
		return nil, fmt.Errorf("srcmap: map is %d bytes (max %d)", len(b), MaxMapBytes)
	}
	var raw struct {
		Version    int      `json:"version"`
		Sources    []string `json:"sources"`
		SourceRoot string   `json:"sourceRoot"`
		Names      []string `json:"names"`
		Mappings   string   `json:"mappings"`
		Sections   []any    `json:"sections"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("srcmap: %w", err)
	}
	if raw.Version != 3 {
		return nil, fmt.Errorf("srcmap: version %d (want 3)", raw.Version)
	}
	if raw.Sections != nil {
		return nil, errors.New("srcmap: index maps (sections) are not supported")
	}
	m := &Map{Sources: raw.Sources, Names: raw.Names, mappings: raw.Mappings}
	if raw.SourceRoot != "" {
		for i, s := range m.Sources {
			m.Sources[i] = strings.TrimSuffix(raw.SourceRoot, "/") + "/" + s
		}
	}
	// One pass: record each line's checkpoint, validating as we go.
	var st checkpoint
	m.lines = append(m.lines, st)
	s := raw.Mappings
	for i := 0; i < len(s); {
		switch s[i] {
		case ';':
			i++
			st.off = i
			m.lines = append(m.lines, st)
			continue
		case ',':
			i++
			continue
		}
		var seg [5]int32
		n, next, err := decodeSegment(s, i, &seg)
		if err != nil {
			return nil, err
		}
		i = next
		if n >= 4 {
			st.src += seg[1]
			st.line += seg[2]
			st.col += seg[3]
			if st.src < 0 || int(st.src) >= len(m.Sources) || st.line < 0 || st.col < 0 {
				return nil, errors.New("srcmap: mapping out of range")
			}
		}
		if n == 5 {
			st.nameIx += seg[4]
			if st.nameIx < 0 || int(st.nameIx) >= len(m.Names) {
				return nil, errors.New("srcmap: name out of range")
			}
		}
	}
	return m, nil
}

// Lookup maps a generated position (1-based line and column, as in a V8
// stack frame) to its source: the last segment at or before the column.
func (m *Map) Lookup(line, col int) (Pos, bool) {
	if m == nil || line < 1 || line > len(m.lines) || col < 1 {
		return Pos{}, false
	}
	cp := m.lines[line-1]
	st := cp
	var gen int32
	var best *Pos
	s := m.mappings
	for i := cp.off; i < len(s) && s[i] != ';'; {
		if s[i] == ',' {
			i++
			continue
		}
		var seg [5]int32
		n, next, err := decodeSegment(s, i, &seg)
		if err != nil {
			return Pos{}, false
		}
		i = next
		gen += seg[0]
		if n >= 4 {
			st.src += seg[1]
			st.line += seg[2]
			st.col += seg[3]
		}
		if n == 5 {
			st.nameIx += seg[4]
		}
		if int(gen) > col-1 {
			break
		}
		if n >= 4 {
			p := Pos{Source: m.Sources[st.src], Line: int(st.line) + 1, Column: int(st.col) + 1}
			if n == 5 {
				p.Name = m.Names[st.nameIx]
			}
			best = &p
		} else {
			best = nil // an unmapped segment covers the column
		}
	}
	if best == nil {
		return Pos{}, false
	}
	return *best, true
}

// decodeSegment decodes the base64 VLQ fields of one segment starting at
// s[i]; it returns how many (1, 4 or 5) and where the segment ends.
func decodeSegment(s string, i int, seg *[5]int32) (int, int, error) {
	n := 0
	for i < len(s) && s[i] != ',' && s[i] != ';' {
		if n == 5 {
			return 0, 0, errors.New("srcmap: segment has more than 5 fields")
		}
		var v, shift int64
		for {
			if i >= len(s) {
				return 0, 0, errors.New("srcmap: truncated VLQ")
			}
			d := b64[s[i]]
			if d < 0 {
				return 0, 0, fmt.Errorf("srcmap: bad VLQ character %q", s[i])
			}
			i++
			v |= int64(d&31) << shift
			shift += 5
			if shift > 35 {
				return 0, 0, errors.New("srcmap: VLQ too long")
			}
			if d&32 == 0 {
				break
			}
		}
		if v&1 != 0 {
			v = -(v >> 1)
		} else {
			v >>= 1
		}
		seg[n] = int32(v)
		n++
	}
	if n != 1 && n != 4 && n != 5 {
		return 0, 0, fmt.Errorf("srcmap: segment with %d fields", n)
	}
	return n, i, nil
}

var b64 [256]int8

func init() {
	for i := range b64 {
		b64[i] = -1
	}
	for i, c := range "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/" {
		b64[c] = int8(i)
	}
}

// frameRE finds a location in a stack frame: "file:line:col", either
// bare ("at file:1:2") or in parentheses ("at fn (file:1:2)").
var frameRE = regexp.MustCompile(`([^\s()]+):(\d+):(\d+)`)

// Resolver returns the map for a module named in a stack frame (as
// workerd names it: "dist/index.js"), or nil.
type Resolver func(module string) *Map

// MapStack rewrites every frame location in stack that resolve has a map
// for to its source position. Frames it can't map are left as they are.
func MapStack(stack string, resolve Resolver) string {
	if resolve == nil || !strings.Contains(stack, ":") {
		return stack
	}
	return frameRE.ReplaceAllStringFunc(stack, func(loc string) string {
		sm := frameRE.FindStringSubmatch(loc)
		m := resolve(ModuleName(sm[1]))
		if m == nil {
			return loc
		}
		line, _ := strconv.Atoi(sm[2])
		col, _ := strconv.Atoi(sm[3])
		p, ok := m.Lookup(line, col)
		if !ok {
			return loc
		}
		return p.Source + ":" + strconv.Itoa(p.Line) + ":" + strconv.Itoa(p.Column)
	})
}

// ModuleName normalises a frame's file to a bundle path: no file:// or
// worker:// scheme, no leading "/" or "./".
func ModuleName(f string) string {
	if i := strings.Index(f, "://"); i > 0 {
		f = f[i+3:]
	}
	f = strings.TrimPrefix(f, "./")
	return strings.TrimLeft(f, "/")
}
