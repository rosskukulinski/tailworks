package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func nopLogf(string, ...any) {}

// A mgmt listener failure after start makes the single-process supervisor
// return an error, which run() turns into exit 1, so systemd
// (Restart=on-failure) and launchd (KeepAlive SuccessfulExit=false)
// restart it (#110).
func TestWaitSingleListenerFailureExitsNonZero(t *testing.T) {
	errs := make(chan error, 1)
	errs <- errors.New("tsnet:443: use of closed network connection")
	err := waitSingle(context.Background(), errs, nopLogf)
	var ce *cliErr
	if !errors.As(err, &ce) || ce.Code != "mgmt_failed" {
		t.Fatalf("waitSingle = %v, want a mgmt_failed cliErr", err)
	}
	if !strings.Contains(strings.Join(ce.Lines, "\n"), "tsnet:443") {
		t.Errorf("detail lacks the listener error: %+v", ce)
	}
}

// A shutdown signal (tw down, systemctl stop) is the clean stop: exit 0.
func TestWaitSingleSignalExitsZero(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitSingle(ctx, make(chan error), nopLogf); err != nil {
		t.Fatalf("waitSingle after a signal = %v, want nil", err)
	}
	// A listener error racing the signal also counts as the clean stop.
	for i := 0; i < 20; i++ { // select picks either ready case: try both
		errs := make(chan error, 1)
		errs <- errors.New("closed")
		if err := waitSingle(ctx, errs, nopLogf); err != nil {
			t.Fatalf("listener error during shutdown = %v, want nil", err)
		}
	}
}

func TestSupStop(t *testing.T) {
	s := newSupStop()
	s.stop(nil)
	s.stop(errors.New("late")) // only the first stop counts
	<-s.done
	if s.err != nil {
		t.Errorf("signal then give-up: err %v, want nil", s.err)
	}
	s = newSupStop()
	s.stop(gaveUpErr(&child{name: "tw-hostd", logPath: "/x/hostd.log"}))
	s.stop(nil)
	<-s.done
	var ce *cliErr
	if !errors.As(s.err, &ce) || ce.Code != "host_failed" {
		t.Errorf("give-up err %v, want host_failed", s.err)
	}
	if ce := gaveUpErr(&child{name: "tw-mgmt"}).(*cliErr); ce.Code != "mgmt_failed" {
		t.Errorf("tw-mgmt give-up code %q, want mgmt_failed", ce.Code)
	}
}
