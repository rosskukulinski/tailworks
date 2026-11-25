package gen

import (
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/netd"
)

// TestDocBaseMatchesNetd: the runtime wrapper's error doc_url prefix (svc.js
// DOC, imported by platform.js) is the same as the Go side's, so it names the
// default branch (#365).
func TestDocBaseMatchesNetd(t *testing.T) {
	want := `export const DOC = "` + netd.DocBase + `";`
	if !strings.Contains(svcJS, want) {
		t.Errorf("svc.js does not declare %s", want)
	}
	if strings.Contains(platformJS, "const DOC =") {
		t.Error("platform.js declares its own DOC; import it from tw-svc.js")
	}
	for name, src := range map[string]string{"svc.js": svcJS, "platform.js": platformJS} {
		if strings.Contains(src, "/blob/main/") {
			t.Errorf("%s links to blob/main; the default branch is master", name)
		}
	}
}
