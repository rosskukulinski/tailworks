package main

import (
	"bytes"
	"flag"
	"log/slog"
	"strings"
	"testing"
)

// -default-tier is hidden from -help; process is ignored with a warning and
// microvm is refused, never silently downgraded.
func TestHostdDeprecatedDefaultTier(t *testing.T) {
	fs := flag.NewFlagSet("tw-hostd", flag.ContinueOnError)
	var out bytes.Buffer
	fs.SetOutput(&out)
	fs.String("mgmt", "", "tw-mgmt address")
	tier := hostdDeprecatedFlags(fs)
	fs.Usage()
	if strings.Contains(out.String(), "default-tier") || !strings.Contains(out.String(), "-mgmt") {
		t.Errorf("usage:\n%s", out.String())
	}
	if err := fs.Parse([]string{"-default-tier", "microvm"}); err != nil || *tier != "microvm" {
		t.Fatalf("parse: %v %q", err, *tier)
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	if err := hostdCheckDefaultTier("", log); err != nil {
		t.Errorf("unset: %v", err)
	}
	if err := hostdCheckDefaultTier("process", log); err != nil || !strings.Contains(logs.String(), "deprecated") {
		t.Errorf("process: %v, logs %q", err, logs.String())
	}
	if err := hostdCheckDefaultTier("microvm", log); err == nil || !strings.Contains(err.Error(), "tw tenant set") {
		t.Errorf("microvm: %v", err)
	}
}
