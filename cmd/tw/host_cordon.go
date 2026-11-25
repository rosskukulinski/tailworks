package main

import (
	"context"
	"fmt"
	"time"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// tw host cordon | uncordon | drain (ADR-041 §5).

func cmdHostCordon(args []string, on bool) error {
	name := "host uncordon"
	if on {
		name = "host cordon"
	}
	f := newFlags(name)
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fail("bad_request", "tw "+name+" needs one host ID", "usage: tw "+usageOf("host"))
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	method := "DELETE"
	if on {
		method = "PUT"
	}
	var h map[string]any
	if err := s.mgmt().DoHeaders(ctx, method, "/v1/hosts/"+pos[0]+"/cordon", nil, &h, map[string]string{"Tw-Actor": actor()}); err != nil {
		return err
	}
	emit(f, h, func() {
		if on {
			fmt.Printf("✓ host     %s cordoned: it keeps what it runs; nothing new is placed there (tw host uncordon %s)\n", pos[0], pos[0])
		} else {
			fmt.Printf("✓ host     %s uncordoned\n", pos[0])
		}
	})
	return nil
}

func cmdHostDrain(args []string) error {
	f := newFlags("host drain")
	loss := f.Bool("allow-local-state-loss", false, "also move workloads with Durable Objects (their storage stays on this host; they start empty elsewhere)")
	dry := f.Bool("dry-run", false, "show what would move; change nothing")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fail("bad_request", "tw host drain needs one host ID", "usage: tw "+usageOf("host"))
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var res mgmt.DrainResult
	if err := s.mgmt().DoHeaders(ctx, "POST", "/v1/hosts/"+pos[0]+"/drain", mgmt.DrainOptions{AllowLocalStateLoss: *loss, DryRun: *dry}, &res,
		map[string]string{"Tw-Actor": actor()}); err != nil {
		return err
	}
	emit(f, res, func() {
		verb := "moved"
		if res.DryRun {
			verb = "would move"
		}
		if res.Cordoned {
			fmt.Printf("✓ host     %s cordoned\n", res.HostID)
		}
		for _, m := range res.Moved {
			fmt.Printf("✓ %s %s/%s → %s\n", verb, m.TenantID, m.Workload, m.To)
		}
		for _, r := range res.Refused {
			fmt.Printf("· stays    %s/%s [%s] %s\n", tsafe(r.TenantID), tsafe(r.Workload), tsafe(r.Code), tsafe(r.Message))
		}
		if len(res.Moved) == 0 && len(res.Refused) == 0 {
			fmt.Println("  nothing runs there")
		}
	})
	return nil
}
