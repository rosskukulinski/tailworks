package main_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/mgmt"
)

func (f *fakeTS) revokedKeys() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, revoked := range f.keys {
		if revoked {
			n++
		}
	}
	return n
}

// downRevokesWarmKeys runs `tw down` (#158, H25): `tw status` counts the
// tenant's warm keys in custody first, and after the down every one was
// revoked at the fake Tailscale API and mgmt's store holds none.
func downRevokesWarmKeys(t *testing.T, e *cliflow.Env, fts *fakeTS) {
	t.Helper()
	var st struct {
		WarmKeys int `json:"warmKeys"`
		Tenants  []struct {
			WarmKeys int `json:"warmKeys"`
		} `json:"tenants"`
	}
	e.MustJSON(&st, "status")
	if len(st.Tenants) == 0 {
		t.Fatalf("tw status: warmKeys %d, tenants %+v", st.WarmKeys, st.Tenants)
	}
	before := fts.revokedKeys()
	e.Down()
	if got := fts.revokedKeys() - before; got < st.WarmKeys {
		t.Fatalf("tw down revoked %d keys; tw status showed %d warm keys in custody", got, st.WarmKeys)
	}
	s, err := mgmt.OpenStore(context.Background(), filepath.Join(e.StateDir, "mgmt", "mgmt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if ks, err := s.ListWarmKeys(context.Background(), ""); err != nil || len(ks) > 0 {
		t.Fatalf("after tw down: warm keys %+v (%v)", ks, err)
	}
	t.Logf("tw down revoked %d warm keys", st.WarmKeys)
}
