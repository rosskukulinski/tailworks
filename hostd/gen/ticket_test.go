package gen

import (
	"strings"
	"testing"
	"time"
)

func TestCreateTicket(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tk := CreateTicket("rootkey", "tr-1", now)
	if tr, ok := VerifyTicket("rootkey", tk, now.Add(30*time.Second)); !ok || tr != "tr-1" {
		t.Fatalf("fresh ticket: %v %q", ok, tr)
	}
	for name, bad := range map[string]string{
		"expired":    CreateTicket("rootkey", "tr-1", now.Add(-TicketTTL-time.Second)),
		"other key":  CreateTicket("otherkey", "tr-1", now),
		"tampered":   strings.Replace(tk, "tr-1", "tr-2", 1),
		"future":     "v1." + "9999999999999" + ".tr-1." + ticketMAC("rootkey", "9999999999999", "tr-1"),
		"garbage":    "v1.x.y",
		"empty":      "",
		"no trace":   "v1." + tk[3:16] + "..abc",
		"wrong vers": "v0" + tk[2:],
	} {
		if _, ok := VerifyTicket("", tk, now); ok {
			t.Error("empty root verified")
		}
		if _, ok := VerifyTicket("rootkey", bad, now); ok {
			t.Errorf("%s ticket verified", name)
		}
	}
}
