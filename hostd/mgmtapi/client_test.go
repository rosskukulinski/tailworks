package mgmtapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestWithHostIDLeavesClientAlone: WithHostID returns a copy with the new
// identity and never writes the original, which other goroutines may be
// using (issue #135; run under -race).
func TestWithHostIDLeavesClientAlone(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Header.Get("Tw-Host-ID")+"|"+r.Header.Get(HostTokenHeader)]++
		mu.Unlock()
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.HostID, c.Token = "h1", "tok"

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if err := c.Do(context.Background(), "GET", "/", nil, nil); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	h2 := c.WithHostID("h2")
	if err := h2.Do(context.Background(), "GET", "/", nil, nil); err != nil {
		t.Fatal(err)
	}
	wg.Wait()

	if c.HostID != "h1" || h2.HostID != "h2" || h2.Base != c.Base || h2.HTTP != c.HTTP || h2.Token != c.Token {
		t.Fatalf("original %+v, copy %+v", c, h2)
	}
	if seen["h1|tok"] != 80 || seen["h2|tok"] != 1 || len(seen) != 2 {
		t.Fatalf("headers seen: %v", seen)
	}
}
