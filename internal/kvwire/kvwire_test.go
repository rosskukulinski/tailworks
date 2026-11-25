package kvwire

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

// TestEncodePinned pins the entry format hostd/gen/js/kv.js writes (the
// hostd KV tests check the two interoperate through a real journal).
func TestEncodePinned(t *testing.T) {
	got := hex.EncodeToString(Encode(Header{Key: "k", Expiration: 7, Metadata: `{"a":1}`}, []byte("v")))
	// 01 | len 0x1f | {"k":"k","x":7,"m":"{\"a\":1}"} | v
	want := "010000001f" + hex.EncodeToString([]byte(`{"k":"k","x":7,"m":"{\"a\":1}"}`)) + "76"
	if got != want {
		t.Fatalf("encode: %s\nwant    %s", got, want)
	}
	e, err := Decode(Encode(Header{Key: "gone", Delete: true}, nil))
	if err != nil || !e.Delete || e.Key != "gone" || len(e.Value) != 0 {
		t.Fatalf("delete round trip: %+v %v", e, err)
	}
	for _, bad := range [][]byte{nil, {2, 0, 0, 0, 0}, {1, 0, 0, 0, 9, '{'}, {1, 0, 0, 0, 1, '{'}} {
		if _, err := Decode(bad); err == nil {
			t.Errorf("decoded %x", bad)
		}
	}
}

func TestFoldAndPage(t *testing.T) {
	var j [][]byte
	put := func(k, v string, x int64) { j = append(j, Encode(Header{Key: k, Expiration: x}, []byte(v))) }
	put("a", "1", 0)
	put("b", "2", 0)
	put("a", "1b", 0)
	j = append(j, Encode(Header{Key: "b", Delete: true}, nil))
	put("old", "x", 100)
	put("new", "y", 300)
	for i := 0; i < 5; i++ {
		put(fmt.Sprintf("p/%d", i), "", 0)
	}
	items, err := Fold(j, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 7 || string(items["a"].Value) != "1b" || items["new"].Expiration != 300 {
		t.Fatalf("fold: %+v", items)
	}
	if _, ok := items["old"]; ok {
		t.Fatal("expired key kept")
	}
	var pages [][]string
	cur := ""
	for {
		p, next := Page(items, "p/", cur, 2)
		var ks []string
		for _, it := range p {
			ks = append(ks, it.Key)
		}
		pages = append(pages, ks)
		if next == "" {
			break
		}
		cur = next
	}
	if fmt.Sprint(pages) != "[[p/0 p/1] [p/2 p/3] [p/4]]" {
		t.Fatalf("pages: %v", pages)
	}
}

func TestChecks(t *testing.T) {
	for _, k := range []string{"", ".", "..", strings.Repeat("k", MaxKeyBytes+1), "\xff"} {
		if CheckKey(k) == nil {
			t.Errorf("key %q accepted", k)
		}
	}
	if CheckKey(strings.Repeat("k", MaxKeyBytes)) != nil || CheckKey("a/b c") != nil {
		t.Error("good key refused")
	}
	if CheckMetadata(`{"a":`) == nil || CheckMetadata(`"`+strings.Repeat("m", MaxMetadataBytes)+`"`) == nil || CheckMetadata(`{"a":1}`) != nil {
		t.Error("metadata checks")
	}
	if !bytes.Equal(Encode(Header{Key: "k"}, nil)[5:], []byte(`{"k":"k"}`)) {
		t.Error("minimal header")
	}
}
