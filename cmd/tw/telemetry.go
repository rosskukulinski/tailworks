package main

// `tw telemetry show|set|rm` (ADR-045): the tenant's OTLP
// export. Every workload of the tenant then exports its request records,
// metrics and spans with OTLP/HTTP (JSON) from its own tailnet node, so the
// collector must be a tailnet peer the policy lets the workloads reach.
// Header values (collector credentials) go to mgmt's custody and are never
// printed: give them as --header NAME (the value is read from stdin, a
// hidden prompt on a terminal), or --header NAME=VALUE for values that are
// not secret.

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/internal/obs"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// repeated is a flag that may be given several times.
type repeated []string

func (r *repeated) String() string     { return strings.Join(*r, ",") }
func (r *repeated) Set(s string) error { *r = append(*r, s); return nil }

// telemetryOpts are tw telemetry's flags. Only set has its own
// (telemetryFlags).
type telemetryOpts struct {
	tenant *string

	// set
	otlp, signals *string
	headers       *repeated
}

// telemetryFlags registers verb's flags (anyVerb: every verb's) on f.
func telemetryFlags(f *flags, verb string) *telemetryOpts {
	o := &telemetryOpts{otlp: new(string), signals: new(string), headers: &repeated{}}
	o.tenant = tenantFlag(f)
	if onVerb(verb, "set") {
		o.otlp = f.String("otlp", "", "the OTLP/HTTP base URL of a collector on the tailnet (http://otel-collector:4318)")
		f.Var(o.headers, "header", "a header for every export, NAME=VALUE, or NAME alone to read its value from stdin (repeatable)")
		o.signals = f.String("signals", "", "what to export, comma-separated (default logs,metrics,traces)")
	}
	return o
}

func cmdTelemetry(args []string) error {
	sub, rest := scanVerbOr(args, func(f *flags) { telemetryFlags(f, anyVerb) }, "show")
	f := newFlags("telemetry")
	o := telemetryFlags(f, sub)
	tenantF, endpoint, signals := o.tenant, o.otlp, o.signals
	pos, err := f.parse(rest)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return fail("bad_request", "unexpected arguments", "usage: tw "+usageOf("telemetry"))
	}
	headers := *o.headers
	var put mgmtapi.OTLPExport
	switch sub {
	case "show", "ls":
		sub = "show"
	case "rm", "off":
		sub = "rm"
	case "set":
		put.Endpoint = *endpoint
		if err := obs.ValidateEndpoint(put.Endpoint); err != nil {
			return fail("bad_request", "--otlp: "+err.Error()).withFix("tw telemetry set --otlp http://otel-collector:4318")
		}
		if *signals != "" {
			put.Signals = strings.Split(*signals, ",")
		}
		put.Headers = map[string]string{}
		for _, h := range headers {
			name, value, ok := strings.Cut(h, "=")
			if !ok {
				if value, err = readHeaderValue(name); err != nil {
					return err
				}
			}
			put.Headers[strings.TrimSpace(name)] = value
		}
	default:
		return fail("bad_request", fmt.Sprintf("unknown subcommand %q (show, set or rm)", sub), "usage: tw "+usageOf("telemetry"))
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if err := s.requireUp(); err != nil {
		return err
	}
	tn, err := s.tenant(*tenantF)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	path := "/v1/tenants/" + url.PathEscape(tn.TenantID) + "/telemetry"
	hdr := map[string]string{"Tw-Actor": actor()}
	var v mgmtapi.TelemetryView
	switch sub {
	case "show":
		if err := s.mgmt().Do(ctx, "GET", path, nil, &v); err != nil {
			return err
		}
	case "set":
		if err := s.mgmt().DoHeaders(ctx, "PUT", path, map[string]any{"otlp": put}, &v, hdr); err != nil {
			return err
		}
	case "rm":
		if err := s.mgmt().DoHeaders(ctx, "DELETE", path, nil, nil, hdr); err != nil {
			return err
		}
		v.TenantID = tn.TenantID
	}
	emit(f, v, func() {
		switch {
		case sub == "rm":
			fmt.Printf("✓ OTLP export off for tenant %s (hosts stop within a minute; tw logs and /metrics keep working)\n", tn.Tailnet)
		case v.OTLP == nil:
			fmt.Printf("tenant %s exports nothing over OTLP · request records: tw logs <workload> · metrics: GET /metrics on port %d of each host's node\n", tn.Tailnet, obs.DefaultMetricsPort)
			fmt.Printf("  export: tw telemetry set --otlp http://<collector>:4318 [--header authorization]\n")
		default:
			if sub == "set" {
				fmt.Printf("✓ OTLP export set for tenant %s (hosts pick it up within a minute)\n", tn.Tailnet)
			}
			fmt.Printf("  endpoint  %s\n  signals   %s\n", v.OTLP.Endpoint, strings.Join(v.OTLP.Signals, ", "))
			if len(v.OTLP.HeaderNames) > 0 {
				fmt.Printf("  headers   %s (values are never shown)\n", strings.Join(v.OTLP.HeaderNames, ", "))
			}
			fmt.Printf("  each workload exports from its own tailnet node: the policy must let it reach %s\n", hostOfURL(v.OTLP.Endpoint))
		}
	})
	return nil
}

// readHeaderValue reads one header value from stdin (hidden prompt on a
// terminal), like a secret.
func readHeaderValue(name string) (string, error) {
	v, err := readSecretValue("header " + name)
	if err != nil {
		return "", err
	}
	if len(v) > mgmt.TelemetryMaxHeaderValue {
		return "", fail("bad_request", fmt.Sprintf("the value of header %s is over %d bytes", name, mgmt.TelemetryMaxHeaderValue))
	}
	return v, nil
}

func hostOfURL(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return s
	}
	return u.Host
}
