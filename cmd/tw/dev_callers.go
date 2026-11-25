package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rosskukulinski/tailworks/netd"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// tw dev --caller: extra dev client nodes, so a test (or a person) can call
// the app as more than one tailnet identity. Each caller is its own node on
// dev's embedded control plane, untagged (a user's node) or tagged, with its
// own local proxy on 127.0.0.1. Requests through a caller's proxy reach the
// workload over that node, so getCaller() sees it, verified by netd as for
// the default "you" node. Nothing is forged: the identity is the node's.
//
// The embedded control plane names users itself (user-<n>@…); a caller's
// login is reported in the ready event, it can't be chosen.

// devCaller is one --caller in tw dev's result and ready event.
type devCaller struct {
	Name   string   `json:"name"`
	URL    string   `json:"url"`
	As     string   `json:"as,omitempty"`
	Tags   []string `json:"tags,omitempty"`
	NodeID string   `json:"nodeId,omitempty"`
	Node   string   `json:"node,omitempty"`
}

type devCallerSpec struct {
	name string
	tags []string
}

// callerFlags is the repeatable --caller <name>[=<tag>,<tag>…].
type callerFlags []devCallerSpec

var devCallerNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
var devCallerTagRE = regexp.MustCompile(`^tag:[A-Za-z0-9-]+$`)

func (c *callerFlags) String() string {
	var s []string
	for _, x := range *c {
		s = append(s, x.name)
	}
	return strings.Join(s, ",")
}

func (c *callerFlags) Set(v string) error {
	name, tagList, _ := strings.Cut(v, "=")
	if !devCallerNameRE.MatchString(name) || name == "you" {
		return fmt.Errorf("--caller %q: want <name>[=<tag>,…], name of a-z, 0-9 and - (not \"you\")", v)
	}
	sp := devCallerSpec{name: name}
	if tagList != "" {
		for _, t := range strings.Split(tagList, ",") {
			if !devCallerTagRE.MatchString(t) {
				return fmt.Errorf("--caller %s: %q is not a tag (tag:<name>)", name, t)
			}
			sp.tags = append(sp.tags, t)
		}
	}
	for _, x := range *c {
		if x.name == name {
			return fmt.Errorf("--caller %s given twice", name)
		}
	}
	*c = append(*c, sp)
	return nil
}

// startDevCallers brings up one node and one local proxy per --caller
// (in parallel); target is the workload's current tailnet IP (it changes on
// reload). The returned stop closes them all.
func startDevCallers(ctx context.Context, controlURL, tmp string, specs callerFlags, target func() string) ([]devCaller, func(), error) {
	out := make([]devCaller, len(specs))
	nodes := make([]*nettest.Node, len(specs))
	errs := make([]error, len(specs))
	var wg sync.WaitGroup
	for i, sp := range specs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			nodes[i], errs[i] = nettest.NewTaggedNode(ctx, controlURL, nettest.DummyKey, "dev-"+sp.name, filepath.Join(tmp, "caller-"+sp.name), sp.tags)
		}()
	}
	wg.Wait()
	var closers []func()
	stop := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}
	for i, n := range nodes {
		if n != nil {
			closers = append(closers, func() { n.Close() })
		}
		if errs[i] != nil {
			stop()
			return nil, func() {}, fail("identity_pending", "tw dev --caller "+specs[i].name+": "+errs[i].Error())
		}
	}
	for i, sp := range specs {
		n := nodes[i]
		c := devCaller{Name: sp.name, Tags: sp.tags}
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if st, err := n.LC.Status(sctx); err == nil && st.Self != nil {
			c.NodeID = string(st.Self.ID)
			c.Node = strings.TrimSuffix(st.Self.DNSName, ".")
			if len(sp.tags) == 0 {
				if u, ok := st.User[st.Self.UserID]; ok {
					c.As = u.LoginName
				}
			}
		}
		cancel()
		rp := &httputil.ReverseProxy{
			Transport: n.HTTPClient(0).Transport,
			Rewrite: func(pr *httputil.ProxyRequest) {
				ip := target()
				pr.Out.URL.Scheme, pr.Out.URL.Host, pr.Out.Host = "http", ip, ip
				netd.KeepBodyEOF(pr) // #138
			},
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			stop()
			return nil, func() {}, err
		}
		srv := &http.Server{Handler: rp, ReadHeaderTimeout: 10 * time.Second}
		go srv.Serve(ln)
		closers = append(closers, func() { srv.Close() })
		c.URL = "http://" + ln.Addr().String()
		out[i] = c
	}
	return out, stop, nil
}

// printDevCallers is the text output's line per --caller.
func printDevCallers(w io.Writer, cs []devCaller) {
	for _, c := range cs {
		who := c.As
		if len(c.Tags) > 0 {
			who = strings.Join(c.Tags, ",")
		}
		fmt.Fprintf(w, "  caller %-26s %s  (%s)\n", c.Name, c.URL, who)
	}
}
