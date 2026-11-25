package mgmt

import "strings"

// ValidTagSyntax reports whether tag follows Tailscale's tag grammar, so
// the CLI can check a tag before it writes it into a tailworks.jsonc.
func ValidTagSyntax(tag string) bool { return validTagSyntax(tag) }

// IsReservedTag reports whether tag is one of tailworks' own tags, which no
// workload may ask for (deploy answers 400 reserved_tag).
func IsReservedTag(tag string) bool {
	for _, res := range reservedTags {
		if strings.EqualFold(tag, res) {
			return true
		}
	}
	return false
}
