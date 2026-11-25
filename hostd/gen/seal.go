package gen

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// ---- caller seals (RT3-08 / H08, #148) ----
//
// netd puts the WhoIs caller in request.cf.tailscale.caller, but request.cf
// is whatever the sender says once a request is forwarded in-process (a
// Durable Object stub fetch, an RPC argument): tenant code can attach any
// caller it likes. So hostd seals the caller of every tailnet request it
// forwards to workerd under the instance root key (MAC_KEY, the same key
// that signs egress and create tickets; it is only ever in hostd and the
// trusted platform worker). The platform (facet objects, principalOf) and,
// through the platform, the runtime (the Worker, plain DOs) honour a caller
// only if its seal verifies, and then use the sealed copy: a forged or
// swapped caller is "no caller". See docs/security-model.md for what a seal
// does not stop (replaying the same real caller within a workload).

// CallerSealTTL bounds how long a sealed caller is honoured after hostd
// forwarded the request (its in-process forwards happen within it).
const CallerSealTTL = 5 * time.Minute

const sealDomain = "tw-caller-seal-v1\n"

// CallerSeal is request.cf.tailscale.seal. Caller is the exact JSON the MAC
// covers; verifiers parse it rather than trusting cf.tailscale.caller.
type CallerSeal struct {
	V      int    `json:"v"`
	Exp    int64  `json:"exp"` // unix ms
	Caller string `json:"caller"`
	MAC    string `json:"mac"`
}

func sealMAC(root string, exp int64, caller string) string {
	m := hmac.New(sha256.New, []byte(root))
	m.Write([]byte(sealDomain + strconv.FormatInt(exp, 10) + "\n" + caller))
	return hex.EncodeToString(m.Sum(nil))
}

// SealCaller seals caller (its JSON) under root, valid until now +
// CallerSealTTL: hex(HMAC-SHA256(root, "tw-caller-seal-v1\n" + exp + "\n" +
// caller)). The platform verifies the same MAC in JS (platform.js
// verifiedCaller). An empty root seals nothing.
func SealCaller(root string, caller []byte, now time.Time) *CallerSeal {
	if root == "" || len(caller) == 0 {
		return nil
	}
	exp := now.Add(CallerSealTTL).UnixMilli()
	return &CallerSeal{V: 1, Exp: exp, Caller: string(caller), MAC: sealMAC(root, exp, string(caller))}
}

// VerifyCallerSeal reports whether s is a seal root issued that has not
// expired (the Go twin of platform.js verifiedCaller, for tests and hostd).
func VerifyCallerSeal(root string, s *CallerSeal, now time.Time) bool {
	if root == "" || s == nil || s.V != 1 || s.Caller == "" {
		return false
	}
	exp := time.UnixMilli(s.Exp)
	if now.After(exp) || exp.Sub(now) > CallerSealTTL+time.Minute {
		return false
	}
	return hmac.Equal([]byte(sealMAC(root, s.Exp, s.Caller)), []byte(s.MAC))
}
