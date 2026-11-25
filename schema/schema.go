// Package schema embeds tailworks.schema.json, the JSON Schema of tailworks.jsonc, and
// checks a config against it. The schema is the single list of tailworks.jsonc
// keys: the Go structs that read tailworks.jsonc (mgmt, hostd/bundle, placement,
// containers) each decode only the part they need, so none of them can
// reject a misspelt key on its own.
//
// Check implements the subset of JSON Schema the file uses: type, enum,
// pattern, minimum, required, properties, additionalProperties (false or a
// schema), items and local $ref. That is enough to catch typos, which are
// silent otherwise ("objcts" would deploy every class without a node).
package schema

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/tailscale/hujson"
)

//go:embed tailworks.schema.json
var TwSchema []byte

// Problem is one schema violation, at a dotted path like
// "tailnet.objects.SupportAgent.tags".
type Problem struct {
	Path    string
	Message string
	// Suggest is the known key closest to an unknown one ("" if none).
	Suggest string
}

func (p Problem) String() string {
	at := p.Path
	if at == "" {
		at = "(top level)"
	}
	s := at + ": " + p.Message
	if p.Suggest != "" {
		s += fmt.Sprintf(" (did you mean %q?)", p.Suggest)
	}
	return s
}

type node struct {
	Ref                  string           `json:"$ref"`
	Type                 json.RawMessage  `json:"type"`
	Enum                 []any            `json:"enum"`
	Pattern              string           `json:"pattern"`
	Minimum              *float64         `json:"minimum"`
	Required             []string         `json:"required"`
	Properties           map[string]*node `json:"properties"`
	AdditionalProperties json.RawMessage  `json:"additionalProperties"`
	Items                *node            `json:"items"`
	Defs                 map[string]*node `json:"$defs"`
}

var root = func() *node {
	var n node
	if err := json.Unmarshal(TwSchema, &n); err != nil {
		panic("schema: tailworks.schema.json: " + err.Error())
	}
	return &n
}()

// CheckTw checks a tailworks.jsonc (JSONC/HuJSON) against the schema. A syntax
// error is returned as err; schema violations as problems, sorted by path.
func CheckTw(raw []byte) ([]Problem, error) {
	std, err := hujson.Standardize(append([]byte(nil), raw...))
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(std, &v); err != nil {
		return nil, err
	}
	var out []Problem
	check(root, root, v, "", &out)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// resolve follows local $refs against r, the schema document's root.
func resolve(r, n *node) *node {
	for n != nil && n.Ref != "" {
		name, ok := strings.CutPrefix(n.Ref, "#/$defs/")
		if !ok {
			panic("schema: unsupported $ref " + n.Ref)
		}
		n = r.Defs[name]
	}
	return n
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func check(r, n *node, v any, path string, out *[]Problem) {
	n = resolve(r, n)
	if n == nil {
		return
	}
	if !typeOK(n.Type, v) {
		*out = append(*out, Problem{Path: path, Message: fmt.Sprintf("is %s, want %s", typeName(v), wantTypes(n.Type))})
		return
	}
	if len(n.Enum) > 0 && !inEnum(n.Enum, v) {
		*out = append(*out, Problem{Path: path, Message: fmt.Sprintf("%s is not one of %s", jsonStr(v), enumList(n.Enum)), Suggest: closestEnum(n.Enum, v)})
	}
	switch x := v.(type) {
	case string:
		if n.Pattern != "" && !regexp.MustCompile(n.Pattern).MatchString(x) {
			*out = append(*out, Problem{Path: path, Message: fmt.Sprintf("%q must match %s", x, n.Pattern)})
		}
	case float64:
		if n.Minimum != nil && x < *n.Minimum {
			*out = append(*out, Problem{Path: path, Message: fmt.Sprintf("%v is below the minimum %v", x, *n.Minimum)})
		}
	case []any:
		if n.Items != nil {
			for i, e := range x {
				check(r, n.Items, e, fmt.Sprintf("%s[%d]", path, i), out)
			}
		}
	case map[string]any:
		for _, r := range n.Required {
			if _, ok := x[r]; !ok {
				*out = append(*out, Problem{Path: path, Message: fmt.Sprintf("missing required %q", r)})
			}
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var extra *node
		closed := false
		if len(n.AdditionalProperties) > 0 {
			if string(n.AdditionalProperties) == "false" {
				closed = true
			} else {
				extra = &node{}
				if err := json.Unmarshal(n.AdditionalProperties, extra); err != nil {
					panic("schema: additionalProperties: " + err.Error())
				}
			}
		}
		for _, k := range keys {
			if p, ok := n.Properties[k]; ok {
				check(r, p, x[k], join(path, k), out)
				continue
			}
			if extra != nil {
				check(r, extra, x[k], join(path, k), out)
				continue
			}
			if closed {
				*out = append(*out, Problem{Path: join(path, k), Message: "unknown key", Suggest: closestKey(n.Properties, k)})
			}
		}
	}
}

func types(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}
	var many []string
	_ = json.Unmarshal(raw, &many)
	return many
}

func typeOK(raw json.RawMessage, v any) bool {
	ts := types(raw)
	if len(ts) == 0 {
		return true
	}
	for _, t := range ts {
		switch t {
		case "integer":
			if f, ok := v.(float64); ok && f == float64(int64(f)) {
				return true
			}
		case typeName(v):
			return true
		}
	}
	return false
}

func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	default:
		return "object"
	}
}

func wantTypes(raw json.RawMessage) string {
	ts := types(raw)
	for i, t := range ts {
		if t == "array" || t == "object" || t == "integer" {
			ts[i] = "an " + t
		} else {
			ts[i] = "a " + t
		}
	}
	return strings.Join(ts, " or ")
}

func inEnum(enum []any, v any) bool {
	for _, e := range enum {
		if e == v {
			return true
		}
	}
	return false
}

func jsonStr(v any) string { b, _ := json.Marshal(v); return string(b) }

func enumList(enum []any) string {
	s := make([]string, len(enum))
	for i, e := range enum {
		s[i] = jsonStr(e)
	}
	return strings.Join(s, ", ")
}

func closestEnum(enum []any, v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	cands := map[string]*node{}
	for _, e := range enum {
		if es, ok := e.(string); ok {
			cands[es] = nil
		}
	}
	return closestKey(cands, s)
}

// closestKey returns the candidate within edit distance ⌈len/3⌉ (at least
// 2) of k, preferring case-insensitive matches.
func closestKey(cands map[string]*node, k string) string {
	best, bestD := "", 0
	for c := range cands {
		if strings.EqualFold(c, k) {
			return c
		}
		d := lev(strings.ToLower(c), strings.ToLower(k))
		if best == "" || d < bestD || (d == bestD && c < best) {
			best, bestD = c, d
		}
	}
	limit := max(2, (len(k)+2)/3)
	if best == "" || bestD > limit {
		return ""
	}
	return best
}

func lev(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
