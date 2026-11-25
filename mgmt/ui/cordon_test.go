package ui

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestHostCordon: the host page shows the cordon state inside a live
// region with the matching button, the forms make the CLI's calls, and a
// host an upgrade run cordoned gets a warning instead of Uncordon.
func TestHostCordon(t *testing.T) {
	api := populatedAPI()
	h := serve(t, api)
	post := func(path, form string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://tw-mgmt.t.ts.net"+path, strings.NewReader(form))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	b := get(t, h, "/ui/hosts/h1").Body.String()
	for _, want := range []string{`data-live="cordon"`, `action="/ui/hosts/h1/cordon"`, ">Cordon</button>", "Not cordoned", "Force (kill its objects", "tw host drain h1"} {
		if !strings.Contains(b, want) {
			t.Errorf("uncordoned host page lacks %q", want)
		}
	}
	for _, unwanted := range []string{"Uncordon</button>", "Drain first"} {
		if strings.Contains(b, unwanted) {
			t.Errorf("uncordoned host page has %q", unwanted)
		}
	}

	api.routes["PUT /v1/hosts/h1/cordon"] = map[string]any{"hostID": "h1"}
	api.routes["DELETE /v1/hosts/h1/cordon"] = map[string]any{"hostID": "h1"}
	if w := post("/ui/hosts/h1/cordon", ""); w.Code != 303 || w.Header().Get("Location") != "/ui/hosts/h1?ok=host+h1+cordoned" || api.last() != "PUT /v1/hosts/h1/cordon" {
		t.Fatalf("cordon form: %d %q %s", w.Code, w.Header().Get("Location"), api.last())
	}
	if w := post("/ui/hosts/h1/cordon", "uncordon=true"); w.Code != 303 || w.Header().Get("Location") != "/ui/hosts/h1?ok=host+h1+uncordoned" || api.last() != "DELETE /v1/hosts/h1/cordon" {
		t.Fatalf("uncordon form: %d %q %s", w.Code, w.Header().Get("Location"), api.last())
	}

	rec := api.routes["GET /v1/hosts/h1"].(map[string]any)
	rec["cordonedAt"] = time.Now().UnixMilli() - 60000
	rec["cordonedBy"] = "alice@x"
	b = get(t, h, "/ui/hosts/h1").Body.String()
	for _, want := range []string{"Cordoned", "alice@x", `name="uncordon"`, "Uncordon</button>"} {
		if !strings.Contains(b, want) {
			t.Errorf("cordoned host page lacks %q", want)
		}
	}

	rec["cordonedBy"] = "upgrade:up_1"
	b = get(t, h, "/ui/hosts/h1").Body.String()
	if !strings.Contains(b, "upgrade:up_1") || !strings.Contains(b, "tw upgrade fleet status") || strings.Contains(b, `name="uncordon"`) {
		t.Errorf("upgrade-cordoned host page: want the warning and no Uncordon form")
	}
}
