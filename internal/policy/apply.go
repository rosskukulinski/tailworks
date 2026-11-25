package policy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/tsapi"
)

// ErrConflict: the policy changed between the read and the If-Match write,
// and again on the retry (issue #116). Nothing was written.
var ErrConflict = errors.New("the tailnet policy changed while tailworks was writing it, twice in a row (If-Match failed); nothing was written")

// IsPreconditionFailed reports a write refused because the policy's ETag
// no longer matched (HTTP 412, from Tailscale or Headscale).
func IsPreconditionFailed(err error) bool {
	var e *tsapi.APIError
	return errors.As(err, &e) && e.Status == http.StatusPreconditionFailed
}

// IsForbidden reports a write the credential may not make (HTTP 403: an
// OAuth client without the Policy File write scope).
func IsForbidden(err error) bool {
	var e *tsapi.APIError
	return errors.As(err, &e) && e.Status == http.StatusForbidden
}

// ApplyRetry is ApplyWith for a policy other people edit too: when the
// If-Match write fails because the policy changed since it was read, it
// reads, merges, validates and writes once more. A second conflict is
// ErrConflict. Every attempt goes through Check and VerifyAdditive (RT-06):
// a retry never writes more than the snippet, and never removes anything.
func ApplyRetry(ctx context.Context, api API, s Snippet, o Options) (ApplyResult, error) {
	res, err := ApplyWith(ctx, api, s, o)
	if err == nil || !IsPreconditionFailed(err) {
		return res, err
	}
	res, err = ApplyWith(ctx, api, s, o)
	if err != nil && IsPreconditionFailed(err) {
		return res, fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return res, err
}

// Diff is the snippet as the lines it adds to a policy, each prefixed with
// "+ ". Merge never removes or changes a line, so this is the whole diff.
func (s Snippet) Diff() []string {
	h := strings.TrimRight(s.HuJSON(), "\n")
	if h == "" {
		return nil
	}
	lines := strings.Split(h, "\n")
	for i, l := range lines {
		lines[i] = "+ " + l
	}
	return lines
}
