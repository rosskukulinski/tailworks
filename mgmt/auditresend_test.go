package mgmt

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// TestAuditResendIsIdempotent (#427): a host re-sends a batch when its POST
// /v1/audit failed after mgmt stored it (a client timeout under load). mgmt
// skips what it already has, also when the batch starts a new chain
// segment (prevHash ""), which it used to store twice: the chain then
// broke at the copy, as after an unlinked restart.
func TestAuditResendIsIdempotent(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	h1 := e.asHost("h1")
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	h1.ok("PUT", "/v1/hosts/h1", map[string]any{"capacityNodes": 10, "tiers": []string{"process"}, "auditPubKey": base64.StdEncoding.EncodeToString(pub)}, nil)
	mk := func(kind, prev string) map[string]any {
		r := map[string]any{"ts": time.Now().UTC().Format(time.RFC3339Nano), "kind": kind, "traceID": "tr-resend", "prevHash": prev}
		b, _ := json.Marshal(r)
		h, err := HashRecord(b)
		if err != nil {
			t.Fatal(err)
		}
		r["hash"] = h
		r["hostSig"] = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(h)))
		return r
	}
	send := func(recs ...map[string]any) {
		t.Helper()
		rs := make([]any, len(recs))
		for i, r := range recs {
			rs[i] = r
		}
		h1.ok("POST", "/v1/audit", map[string]any{"records": rs}, nil)
	}
	chain := func() ChainStatus { return e.m.VerifyChain(context.Background(), "host:h1") }

	// The chain's first record, sent twice.
	a := mk("kill", "")
	send(a)
	send(a)
	if st := chain(); !st.OK || st.Records != 1 {
		t.Fatalf("first record re-sent: %+v (want 1 record, verified)", st)
	}
	// A two-record batch from the start, re-sent: only the new one lands.
	b := mk("kill", a["hash"].(string))
	send(a, b)
	if st := chain(); !st.OK || st.Records != 2 {
		t.Fatalf("batch re-sent: %+v (want 2 records, verified)", st)
	}
	// A new segment (a restart that did not continue the chain) is stored
	// and reported, once.
	c := mk("kill", "")
	send(c)
	send(c)
	if st := chain(); st.OK || st.Records != 3 || st.BrokenAt != 3 {
		t.Fatalf("new segment re-sent: %+v (want 3 records, broken at 3)", st)
	}
}
