package ui

import (
	"reflect"
	"strings"
	"testing"
)

func TestErrLines(t *testing.T) {
	bind := `Socket "ingress": bind(): Address already in use (os error 48)`
	in := "workerd exited 6 times: " + bind + "\n" + strings.Repeat(bind+"\n", 4)
	want := []string{"workerd exited 6 times: " + bind + "  (×5)"}
	if got := errLines(in); !reflect.DeepEqual(got, want) {
		t.Errorf("errLines:\n got %q\nwant %q", got, want)
	}
	if got := errSummary("a\nb\nb\nc"); got != "a (+2 more)" {
		t.Errorf("errSummary: %q", got)
	}
}
