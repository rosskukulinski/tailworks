package ui

import (
	"net/http"
	"net/url"
	"strings"
)

// Cordon and uncordon on the host page (issue #465): PUT and DELETE
// /v1/hosts/{id}/cordon, the calls `tw host cordon` and `tw host uncordon`
// make. A cordoned host keeps what it runs and its leases; placement skips
// it. Moving workloads off a host (`tw host drain`) stays in the CLI.

// cordonHostForm cordons the host, or uncordons it when the form says
// uncordon=true.
func (u *ui) cordonHostForm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	r.ParseForm()
	method, msg := "PUT", "host "+id+" cordoned"
	if r.PostForm.Get("uncordon") != "" {
		method, msg = "DELETE", "host "+id+" uncordoned"
	}
	u.mutate(w, r, method, "/v1/hosts/"+url.PathEscape(id)+"/cordon", "/ui/hosts/"+url.PathEscape(id), msg)
}

// Cordoned reports whether the host is cordoned.
func (h host) Cordoned() bool { return h.CordonedAt > 0 || h.CordonedBy != "" }

// CordonedByUpgrade reports whether a `tw upgrade` run cordoned the host.
// The run uncordons it itself when its step is done (or the run is
// aborted), so the page warns instead of offering Uncordon.
func (h host) CordonedByUpgrade() bool { return strings.HasPrefix(h.CordonedBy, "upgrade:") }
