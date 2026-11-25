package obs

import (
	"bytes"
	"io"
	"net/http"
)

// PrometheusContentType is the text exposition format's content type.
const PrometheusContentType = "text/plain; version=0.0.4; charset=utf-8"

// DefaultMetricsPort is where hostd and mgmt serve /metrics on their own
// tailnet node (the OpenTelemetry Prometheus exporter's usual port).
const DefaultMetricsPort = 9464

// MetricsHandler serves GET /metrics from g.
func MetricsHandler(g interface{ WritePrometheus(io.Writer) error }) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var b bytes.Buffer
		if err := g.WritePrometheus(&b); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", PrometheusContentType)
		w.Header().Set("Cache-Control", "no-store")
		w.Write(b.Bytes())
	})
}
