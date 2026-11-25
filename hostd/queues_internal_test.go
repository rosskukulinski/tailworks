package hostd

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// TestInvokeErr: which failures of a batch's POST count as a run (retry,
// attempt counted) and which hand the batch back uncounted (#387: a
// delivery hostd itself cancelled used to count as a failed attempt).
func TestInvokeErr(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancel2 := context.WithTimeout(context.Background(), -time.Second)
	defer cancel2()
	dial := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		err    error
		notRun bool
		want   string
	}{
		{"cancelled by hostd", cancelled, errors.New("Post: context canceled"), true, "delivery cancelled"},
		{"queue() time limit", expired, errors.New("Post: context deadline exceeded"), false, "exceeded the 15m0s limit"},
		{"connection refused", context.Background(), dial, true, "connection refused"},
		{"workerd died mid-run", context.Background(), io.EOF, false, "EOF"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := invokeErr(tc.ctx, tc.err)
			if got := errors.Is(err, errNotRun); got != tc.notRun {
				t.Fatalf("errNotRun = %v, want %v (%v)", got, tc.notRun, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q, want it to mention %q", err, tc.want)
			}
		})
	}
}
