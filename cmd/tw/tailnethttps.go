package main

import (
	"fmt"
	"io"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// tailnetHTTPS reports whether tailworks.jsonc sets tailnet.https
// (#360): the workload node also serves :443 with its
// ts.net certificate.
func tailnetHTTPS(twRaw []byte) bool {
	var top struct {
		Tailnet struct {
			HTTPS bool `json:"https"`
		} `json:"tailnet"`
	}
	return len(twRaw) > 0 && bundle.UnmarshalJSONC(twRaw, &top) == nil && top.Tailnet.HTTPS
}

// workloadURL is the URL tw prints for a deployed workload: https:// with
// tailnet.https, else http:// (both are served then; https is the one
// the app expects to be called at).
func workloadURL(twRaw []byte, magicDNS string) string {
	if tailnetHTTPS(twRaw) {
		return "https://" + magicDNS + "/"
	}
	return "http://" + magicDNS + "/"
}

// printDevTailnetHTTPS: tw dev's local control issues no certificates, so
// a workload with tailnet.https is served on its local http:// URL only,
// and says so once.
func printDevTailnetHTTPS(twRaw []byte, w io.Writer) {
	if tailnetHTTPS(twRaw) {
		fmt.Fprintln(w, "note: tailnet.https is on; tw dev serves the app on its local http:// URL only (request.url is http://); deployed, it is also served on https://<name>.<tailnet>.ts.net")
	}
}
