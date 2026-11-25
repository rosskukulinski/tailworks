package hostd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/gen"
)

// TestLocalTokenConnect: on the Landlock loopback listeners, CONNECT has
// no token header (workerd injects none), so localToken passes it through
// with the expected token for the tunnel's preamble check; other methods
// still need the header.
func TestLocalTokenConnect(t *testing.T) {
	t.Parallel()
	var got string
	h := localToken("tok", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = r.Context().Value(localTokenKey{}).(string)
	}))
	r := httptest.NewRequest(http.MethodConnect, "http://db:5432", nil)
	r.Host = "db:5432"
	h.ServeHTTP(httptest.NewRecorder(), r)
	if got != "tok" {
		t.Fatalf("CONNECT: expected token %q in context", got)
	}
	w := httptest.NewRecorder()
	got = "unset"
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://x/", nil))
	if got != "unset" || w.Code == 200 {
		t.Fatalf("GET without the header reached the handler: %d", w.Code)
	}
	for _, c := range []struct {
		tok, want string
		ok        bool
	}{{"tok", "tok", true}, {"", "tok", false}, {"bad", "tok", false}, {"", "", true}} {
		if gen.ConnectTokenOK(&gen.ConnectPreamble{Token: c.tok}, c.want) != c.ok {
			t.Errorf("ConnectTokenOK(%q, %q) != %v", c.tok, c.want, c.ok)
		}
	}
}
