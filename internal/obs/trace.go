// Package obs is tailworks' observability core (ADR-045): W3C trace
// context, a small metrics registry with Prometheus text exposition,
// OTLP/HTTP (JSON encoding) for logs, metrics and traces, the structured
// request record behind `tw logs`, and the volume bounds (rotating files,
// a rate limiter) that keep one noisy workload from filling a host's disk.
//
// It has no dependencies outside the standard library: tailworks implements the
// OTLP JSON encoding and the Prometheus text format itself rather than
// pulling in the OpenTelemetry SDK and its gRPC/protobuf stack.
package obs

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

// HeaderTraceparent is the W3C trace context header.
const HeaderTraceparent = "traceparent"

// TraceContext is a parsed W3C traceparent (version 00).
type TraceContext struct {
	TraceID string // 32 lowercase hex, not all zero
	SpanID  string // 16 lowercase hex, not all zero (the parent of the receiver's span)
	Flags   string // 2 hex; "01" = sampled
}

// Valid reports whether tc has well-formed, non-zero IDs.
func (tc TraceContext) Valid() bool {
	return isHex(tc.TraceID, 32) && isHex(tc.SpanID, 16) && isHex(tc.Flags, 2)
}

// String formats tc as a traceparent header value.
func (tc TraceContext) String() string {
	f := tc.Flags
	if f == "" {
		f = "01"
	}
	return "00-" + tc.TraceID + "-" + tc.SpanID + "-" + f
}

// ParseTraceparent parses a traceparent header value. Unknown future
// versions are read by their version-00 prefix, as the spec asks; version
// ff and malformed values are rejected.
func ParseTraceparent(s string) (TraceContext, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 55 {
		return TraceContext{}, false
	}
	ver := s[:2]
	if !isHexAny(ver) || ver == "ff" || (ver == "00" && len(s) != 55) || (ver != "00" && len(s) > 55 && s[55] != '-') {
		return TraceContext{}, false
	}
	if s[2] != '-' || s[35] != '-' || s[52] != '-' {
		return TraceContext{}, false
	}
	tc := TraceContext{TraceID: s[3:35], SpanID: s[36:52], Flags: s[53:55]}
	if !tc.Valid() {
		return TraceContext{}, false
	}
	return tc, true
}

// isHex reports whether s is n lowercase hex digits, not all zero.
func isHex(s string, n int) bool {
	if len(s) != n || !isHexAny(s) {
		return false
	}
	if n == 2 {
		return true // flags may be 00
	}
	return strings.Trim(s, "0") != ""
}

func isHexAny(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return s != ""
}

// NewTraceID returns a random 16-byte trace ID (32 hex).
func NewTraceID() string { return randHex(16) }

// NewSpanID returns a random 8-byte span ID (16 hex).
func NewSpanID() string { return randHex(8) }

func randHex(n int) string {
	b := make([]byte, n)
	for {
		rand.Read(b)
		for _, c := range b {
			if c != 0 {
				return hex.EncodeToString(b)
			}
		}
	}
}

// ValidTraceID / ValidSpanID check IDs from less trusted sources (the
// runtime's span reports).
func ValidTraceID(s string) bool { return isHex(s, 32) }
func ValidSpanID(s string) bool  { return isHex(s, 16) }

// SpanKind values (OTLP enum).
const (
	SpanKindInternal = 1
	SpanKindServer   = 2
	SpanKindClient   = 3
)

// Status codes (OTLP enum).
const (
	StatusUnset = 0
	StatusOK    = 1
	StatusError = 2
)

// Span is one finished span.
type Span struct {
	TraceID, SpanID, ParentSpanID string
	Name                          string
	Kind                          int
	Start, End                    time.Time
	Attrs                         []Attr
	Status                        int
	StatusMessage                 string
}

// Attr is one attribute (string, int64, float64, bool or []string value).
type Attr struct {
	Key   string
	Value any
}

// A returns an attribute.
func A(k string, v any) Attr { return Attr{k, v} }
