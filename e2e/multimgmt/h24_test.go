//go:build e2emultimgmt

package multimgmt

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestMM_H24_SecretWritesAcrossReplicas: a workload's secrets are one
// custody entry, changed by read-modify-write. Two writes of the same
// workload through two replicas both read the old set, and the later
// write erased the earlier one, though both were acknowledged (#578, found
// by e2e/dataplane's deploys loop: a deleted secret came back). The fake
// setec answers reads slowly here, so the two read-modify-writes always
// overlap unless something serialises them across replicas.
func TestMM_H24_SecretWritesAcrossReplicas(t *testing.T) {
	r := newRun(t, "H24")
	c := newCluster(t, r, 2)
	up := c.setec.URL
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, rq *http.Request) {
		if rq.URL.Path == "/api/get" {
			time.Sleep(300 * time.Millisecond)
		}
		out, _ := http.NewRequest(rq.Method, up+rq.URL.Path, rq.Body)
		out.Header = rq.Header.Clone()
		resp, err := http.DefaultClient.Do(out)
		if err != nil {
			w.WriteHeader(502)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	t.Cleanup(slow.Close)
	c.setec.URL = slow.URL
	for i := 0; i < 2; i++ {
		c.start(i)
		if _, err := c.waitReady(i, 30*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	s := c.setupTenant(0, 0)
	base := "/v1/tenants/" + s.tenantID + "/workloads/w/secrets/"
	want := map[string]bool{}
	lost := 0
	for round := 0; round < 4; round++ {
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			name := fmt.Sprintf("S%d_%d", round, i)
			want[name] = true
			wg.Add(1)
			go func() {
				defer wg.Done()
				st, b, err := c.call(i, "PUT", base+name, map[string]any{"value": "v-" + name})
				if err != nil || st/100 != 2 {
					t.Errorf("r%d put %s: %d %v %s", i, name, st, err, b)
				}
			}()
		}
		wg.Wait()
		var pg struct{ Items []struct{ Name string } }
		st, b, err := c.call(0, "GET", base[:len(base)-1], nil)
		if err != nil || st != 200 || json.Unmarshal(b, &pg) != nil {
			t.Fatalf("list: %d %v %s", st, err, b)
		}
		have := map[string]bool{}
		for _, it := range pg.Items {
			have[it.Name] = true
		}
		for n := range want {
			if !have[n] {
				lost++
				t.Errorf("round %d: acknowledged secret %s is gone (have %v)", round, n, have)
			}
		}
	}
	t.Logf("%d acknowledged secret writes lost across 4 rounds of 2 concurrent puts through 2 replicas", lost)
}
