package schema

import (
	"encoding/json"
	"reflect"
	"strings"
)

// Unlisted returns the dotted paths of the json-tagged fields of t (a
// struct that decodes tailworks.jsonc, or part of it) that the schema has
// no property for. A struct field the schema doesn't list is a key
// tailworks reads that tw dev and tw deploy refuse as unknown, so the
// readers' tests check this is empty. Map values are "*"; a type with its
// own UnmarshalJSON (or json.RawMessage) is a leaf.
func Unlisted(t reflect.Type) []string {
	var out []string
	walk(root, root, t, "", &out)
	return out
}

var unmarshaler = reflect.TypeFor[json.Unmarshaler]()

func walk(r, n *node, t reflect.Type, path string, out *[]string) {
	n = resolve(r, n)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if n == nil || reflect.PointerTo(t).Implements(unmarshaler) {
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if f.Anonymous && name == "" {
				walk(r, n, f.Type, path, out)
				continue
			}
			if name == "" {
				name = f.Name
			}
			p := join(path, name)
			sub, ok := n.Properties[name]
			if !ok {
				*out = append(*out, p)
				continue
			}
			walk(r, sub, f.Type, p, out)
		}
	case reflect.Map:
		var sub node
		if json.Unmarshal(n.AdditionalProperties, &sub) == nil {
			walk(r, &sub, t.Elem(), join(path, "*"), out)
		}
	case reflect.Slice, reflect.Array:
		if n.Items != nil {
			walk(r, n.Items, t.Elem(), path+"[]", out)
		}
	}
}
