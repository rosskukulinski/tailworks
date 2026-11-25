package proto

import (
	"encoding/json"
	"testing"
)

func TestHelloProtoRoundTrip(t *testing.T) {
	b, err := json.Marshal(Msg{Type: "hello", Token: "t", Proto: 1, GuestVersion: "0.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	var m Msg
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.HelloProto() != 1 || m.GuestVersion != "0.2.0" || m.Token != "t" {
		t.Fatalf("round trip: %+v", m)
	}
	// A hello from a guest before protocol reporting: proto 0 = 1.
	var old Msg
	if err := json.Unmarshal([]byte(`{"type":"hello","token":"t"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.HelloProto() != 1 {
		t.Fatalf("legacy hello proto = %d", old.HelloProto())
	}
	if !UnknownOp(Msg{Error: "unknown op snapshot2"}) || UnknownOp(Msg{Error: "bad"}) {
		t.Fatal("UnknownOp")
	}
}
