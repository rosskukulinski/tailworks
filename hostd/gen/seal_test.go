package gen

import (
	"testing"
	"time"
)

func TestCallerSeal(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	caller := []byte(`{"nodeId":"n1","user":"alice@example.com"}`)
	s := SealCaller("rootkey", caller, now)
	if !VerifyCallerSeal("rootkey", s, now.Add(time.Minute)) {
		t.Fatalf("fresh seal refused: %+v", s)
	}
	if SealCaller("", caller, now) != nil || SealCaller("rootkey", nil, now) != nil {
		t.Fatal("sealed with no key or no caller")
	}
	swapped := *s
	swapped.Caller = `{"nodeId":"nFORGED","user":"ceo@victim.example"}`
	future := *s
	future.Exp = now.Add(time.Hour).UnixMilli()
	future.MAC = sealMAC("rootkey", future.Exp, future.Caller)
	v0 := *s
	v0.V = 0
	for name, bad := range map[string]*CallerSeal{
		"expired":   SealCaller("rootkey", caller, now.Add(-CallerSealTTL-time.Second)),
		"other key": SealCaller("otherkey", caller, now),
		"swapped":   &swapped,
		"future":    &future,
		"version":   &v0,
		"nil":       nil,
		"empty":     {},
	} {
		if VerifyCallerSeal("rootkey", bad, now) {
			t.Errorf("%s seal verified", name)
		}
	}
	if VerifyCallerSeal("", s, now) {
		t.Error("empty root verified")
	}
	// The seal key is the instance root, domain-separated from tickets and
	// per-object egress keys: none of their MACs is a seal MAC.
	if sealMAC("rootkey", 1, "x") == ticketMAC("rootkey", "1", "x") || sealMAC("rootkey", 1, "x") == ObjectMACKey("rootkey", "1", "x") {
		t.Fatal("seal MAC collides with another use of the root key")
	}
}
