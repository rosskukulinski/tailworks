package mgmt

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/policy"
)

// The caller grant (issue #320): the grant that lets every user of the
// tailnet call a new app, which tw policy --apply / tw deploy
// --apply-policy write together with the app's new tag so the first curl
// works without a paste.
//
// It is the one grant mgmt writes whose source is not a tag:tw* tag, so
// RT-06 is relaxed for it alone and as narrowly as it gets:
//
//   - mgmt builds it. Nothing of it but the destination tags is read from
//     the bundle, and grants that come from a bundle (tailnet.connects)
//     still pass policy.Check without the relaxation: a bundle can't ask
//     for an autogroup:member or "*" source.
//   - Its destination is the workload node's own tags, each a tailworks
//     tag that isn't reserved (never tag:tw, tag:tw-mgmt, tag:tw-host or
//     tag:tw-public), on CallerPort only.
//   - It is written only when this same write adds every one of those tags
//     to tagOwners, that is, when the app is new to the policy. A bundle
//     that names another app's tag as its own gets nothing: that tag is
//     already defined. Nor is it added back for an app whose admin removed
//     or narrowed it.
//   - Not when a grant or ACL from people already reaches every node (the
//     default allow-all policy): it would add nothing now and would keep
//     the app open after the admin narrows that rule.

// CallerSrc is who the caller grant lets in: every user of the tailnet
// (not tagged devices).
const CallerSrc = "autogroup:member"

// CallerPort is the port workload nodes serve on (netd's ingress port).
const CallerPort = "tcp:80"

// callerGrantOptions is o with CallerSrc admitted as a grant source, for
// the snippet that carries the caller grant.
func callerGrantOptions(o policy.Options) policy.Options {
	o.AllowSrcs = append(append([]string(nil), o.AllowSrcs...), CallerSrc)
	return o
}

// callerGrant decides whether the caller grant for a workload with these
// tags is written with keep, the snippet about to be added to pol. It
// returns the grant, or nil and, when the admin should add one themselves,
// a line for PolicyApplyResult.Manual ("" when nothing needs saying).
func callerGrant(tags []string, keep policy.Snippet, pol *Policy, o policy.Options) (map[string]any, string) {
	return callerGrantPorts(tags, []string{CallerPort}, keep, pol, o)
}

// callerGrantPorts is callerGrant on these ports (CallerPorts).
func callerGrantPorts(tags, ports []string, keep policy.Snippet, pol *Policy, o policy.Options) (map[string]any, string) {
	if len(tags) == 0 || pol == nil {
		return nil, ""
	}
	g := map[string]any{"src": []string{CallerSrc}, "dst": append([]string(nil), tags...), "ip": append([]string(nil), ports...)}
	manual := func(why string) (map[string]any, string) {
		return nil, fmt.Sprintf("callers: no grant lets people call this app, and tailworks didn't write one (%s); add it, or a narrower one, yourself: %s", why, policy.EntryHuJSON(g))
	}
	if pol.peopleReach("*") {
		return nil, ""
	}
	for _, tag := range tags {
		if pol.peopleReach(tag) {
			return nil, ""
		}
	}
	for _, tag := range tags {
		if !policy.IsTwTag(tag) || IsReservedTag(tag) || tag == PublicTag {
			return manual(tag + " isn't an app tag of tailworks' own")
		}
		if _, defined := pol.TagOwners[tag]; defined {
			return manual(tag + " is already in tagOwners, and tailworks adds this grant only together with a new app's tag")
		}
		if _, adding := keep.TagOwners[tag]; !adding {
			return manual(tag + " isn't being added to tagOwners")
		}
	}
	if err := policy.Check(policy.Snippet{Grants: []map[string]any{g}}, callerGrantOptions(o)); err != nil {
		return manual(checkReason(err))
	}
	return g, ""
}

// onlyCallerGrant reports whether g is the only grant of s with a source
// that isn't a tailworks tag: the last check before a snippet is written
// with callerGrantOptions.
func onlyCallerGrant(s policy.Snippet, g map[string]any) bool {
	gb, _ := json.Marshal(g)
	n := 0
	for _, x := range s.Grants {
		var src struct {
			Src []string `json:"src"`
		}
		xb, _ := json.Marshal(x)
		if json.Unmarshal(xb, &src) != nil {
			return false
		}
		if len(src.Src) > 0 && !fromPeople(src.Src) {
			continue
		}
		if string(xb) != string(gb) {
			return false
		}
		n++
	}
	return n == 1
}

// fromPeople reports whether a rule with these sources admits someone who
// isn't a tailworks node: a user, a group, an autogroup, another tag or "*".
func fromPeople(src []string) bool {
	for _, s := range src {
		if !policy.IsTwTag(s) {
			return true
		}
	}
	return false
}

// peopleReach reports whether some grant (with a network part) or accept
// ACL from people names dst ("*": every node) as a destination.
func (p *Policy) peopleReach(dst string) bool {
	for _, g := range p.Grants {
		if len(g.IP) == 0 || !fromPeople(g.Src) {
			continue
		}
		for _, d := range g.Dst {
			if d == dst {
				return true
			}
		}
	}
	for _, a := range p.ACLs {
		if a.Action != "accept" || !fromPeople(a.Src) {
			continue
		}
		for _, d := range a.Dst {
			// "host:ports"; a tag has a colon of its own.
			if i := strings.LastIndex(d, ":"); i >= 0 && d[:i] == dst {
				return true
			}
		}
	}
	return false
}
