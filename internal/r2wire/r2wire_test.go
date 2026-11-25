package r2wire

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// fold replays one of hostd/gen's R2 index fixtures: entries exactly as
// store-r2.js wrote them, which hostd/gen's TestJSFormatFixtures replays
// with store-r2.js itself.
func fold(t *testing.T, name string, f *Fold) []Object {
	t.Helper()
	fh, err := os.Open(filepath.Join("..", "..", "hostd", "gen", "testdata", "formats", name))
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		if err := f.Add(sc.Bytes()); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	return f.Objects()
}

// TestFixtures: v0 (no v) and v1 fold to the same objects: the multipart
// upload completes big.bin, and docs/hello.txt is put, then deleted.
func TestFixtures(t *testing.T) {
	want := []Object{{Key: "big.bin", Version: "7f3a", Size: 5, ETag: "5d41402abc4b2a76b9719d911017c592", Uploaded: 1763208000000, ContentType: "text/plain"}}
	for _, n := range []string{"r2-index-v0.jsonl", "r2-index-v1.jsonl"} {
		if got := fold(t, n, &Fold{}); !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %+v\nwant %+v", n, got, want)
		}
	}
	if got := fold(t, "r2-index-v1.jsonl", &Fold{Prefix: "docs/"}); len(got) != 0 {
		t.Errorf("prefix docs/: %+v", got)
	}
	if got := fold(t, "r2-index-v1.jsonl", &Fold{After: "big.bin"}); len(got) != 0 {
		t.Errorf("after big.bin: %+v", got)
	}
}

func TestOrderAndWindow(t *testing.T) {
	f := &Fold{Prefix: "a/", After: "a/1"}
	for _, e := range []string{
		`{"v":1,"o":"put","k":"a/3","row":{"size":3}}`,
		`{"v":1,"o":"put","k":"a/1","row":{"size":1}}`,
		`{"v":1,"o":"put","k":"a/2","row":{"size":2}}`,
		`{"v":1,"o":"put","k":"b/1","row":{"size":9}}`,
		`{"v":1,"o":"put","k":"a/4","row":{"size":4}}`,
		`{"v":1,"o":"del","k":"a/4"}`,
		`{"v":1,"o":"put","k":"a/2","row":{"size":22}}`,
	} {
		if err := f.Add([]byte(e)); err != nil {
			t.Fatal(err)
		}
	}
	got := f.Objects()
	if len(got) != 2 || got[0].Key != "a/2" || got[0].Size != 22 || got[1].Key != "a/3" {
		t.Fatalf("%+v", got)
	}
}

// TestNewer: an entry from a newer tailworks is an error, never skipped
// (as store-r2.js's apply throws).
func TestNewer(t *testing.T) {
	for _, e := range []string{`{"v":2,"o":"put","k":"x","row":{}}`, `{"v":1,"o":"compact"}`} {
		if err := (&Fold{}).Add([]byte(e)); !errors.Is(err, ErrNewer) {
			t.Errorf("%s: %v", e, err)
		}
	}
	if err := (&Fold{}).Add([]byte(`not json`)); err == nil || errors.Is(err, ErrNewer) {
		t.Errorf("bad JSON: %v", err)
	}
}
