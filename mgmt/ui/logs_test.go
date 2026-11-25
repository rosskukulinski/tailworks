package ui

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// rawBody is a fake /v1 answer that is not JSON (the logs): it returns the
// Content-Type and the body for the request.
type rawBody func(r *http.Request) (ctype, body string)

// workloadLogs fakes GET /v1/tenants/tn_1/workloads/w/logs for a workload
// with two replicas: h1 answers, h2 does not.
func workloadLogs(now int64) rawBody {
	at := func(ago time.Duration) string {
		return time.UnixMilli(now).Add(-ago).UTC().Format(time.RFC3339Nano)
	}
	recs := []map[string]any{
		{"time": at(30 * time.Second), "kind": "request", "host": "h1", "workload": "w", "object": "obj_w", "method": "GET", "path": "/hello", "status": 200,
			"durationMs": 3.25, "event": "fetch", "outcome": "ok", "traceId": "0af7651916cd43dd8448eb211c80319c", "auditTraceId": "tr_1",
			"caller": map[string]any{"user": "ross@x", "node": "laptop.t.ts.net"},
			"logs":   []any{map[string]any{"time": at(30 * time.Second), "level": "log", "message": "said hello to ross"}}},
		{"time": at(10 * time.Second), "kind": "request", "host": "h1", "workload": "w", "object": "obj_root", "class": "A", "name": "root", "method": "POST", "path": "/boom",
			"status": 500, "durationMs": 1234, "event": "fetch", "outcome": "exception", "traceId": "4bf92f3577b34da6a3ce929d0e0e4736",
			"caller":     map[string]any{"public": true, "ip": "203.0.113.9"},
			"logs":       []any{map[string]any{"level": "error", "message": "about to fail"}},
			"exceptions": []any{map[string]any{"name": "TypeError", "message": "x is undefined", "stack": "TypeError: x is undefined\n    at boom (src/index.ts:12:3)"}}},
		{"time": at(20 * time.Second), "kind": "invocation", "workload": "w", "class": "A", "name": "kid", "event": "alarm", "outcome": "ok"},
	}
	var nd strings.Builder
	nd.WriteString("[h2] tw: host_unavailable: host h2 (holding workload w--r1) did not answer: connection refused\n")
	for _, r := range recs {
		b, _ := json.Marshal(r)
		nd.WriteString("[h1] " + string(b) + "\n")
	}
	console := "[h2] tw: host_unavailable: host h2 (holding workload w--r1) did not answer: connection refused\n" +
		"[h1] workerd starting\n" +
		"[h1] \x1b[31mred alert\x1b[0m done\n"
	return func(r *http.Request) (string, string) {
		if r.URL.Query().Get("source") == "console" {
			return "text/plain; charset=utf-8", console
		}
		return "application/x-ndjson", nd.String()
	}
}

func TestWorkloadLogsRecords(t *testing.T) {
	api := populatedAPI()
	h := serve(t, api)
	w := get(t, h, "/ui/tenants/tn_1/workloads/w/logs")
	body := w.Body.String()
	if w.Code != 200 {
		t.Fatalf("%d\n%s", w.Code, body)
	}
	api.mu.Lock()
	var logsCall string
	for _, c := range api.calls {
		if strings.Contains(c, "/logs?") {
			logsCall = c
		}
	}
	api.mu.Unlock()
	if !strings.Contains(logsCall, "source=requests") || !strings.Contains(logsCall, "n=100") {
		t.Errorf("logs read %q", logsCall)
	}
	for _, want := range []string{
		"GET /hello", "said hello to ross", "3.2 ms", "ross@x", // the 200
		"POST /boom", "x is undefined", "src/index.ts:12:3", "about to fail", "public:203.0.113.9", "1234 ms", // the 500
		`href="/ui/objects/obj_root/logs"`, ">root-abc<", // the node, by name
		"A/kid", "alarm", // an invocation with no object ID
		`href="/ui/audit?trace=tr_1"`, "trace=0af7651916cd43dd8448eb211c80319c", // trace links
		"h2", "did not answer", // the replica that did not answer
		`href="/ui/deployments/dep_1"`, // crumbs
	} {
		if !strings.Contains(body, want) {
			t.Errorf("records page lacks %q", want)
		}
	}
	// Newest first.
	if i, j := strings.Index(body, "POST /boom"), strings.Index(body, "GET /hello"); i < 0 || j < 0 || i > j {
		t.Errorf("records are not newest first (boom at %d, hello at %d)", i, j)
	}
}

func TestWorkloadLogsFiltersReachTheAPI(t *testing.T) {
	api := populatedAPI()
	h := serve(t, api)
	get(t, h, "/ui/tenants/tn_1/workloads/w/logs?n=50&object=A/root&status=5xx&caller=ross&trace=abc&errors=1")
	get(t, h, "/ui/tenants/tn_1/workloads/w/logs?source=console&n=9999&status=5xx")
	api.mu.Lock()
	defer api.mu.Unlock()
	var logs []*http.Request
	for _, r := range api.reqs {
		if strings.HasSuffix(r.URL.Path, "/logs") {
			logs = append(logs, r)
		}
	}
	if len(logs) != 2 {
		t.Fatalf("%d logs reads", len(logs))
	}
	q := logs[0].URL.Query()
	for k, v := range map[string]string{"source": "requests", "n": "50", "object": "A/root", "status": "5xx", "caller": "ross", "trace": "abc", "errors": "true"} {
		if q.Get(k) != v {
			t.Errorf("records read: %s=%q, want %q", k, q.Get(k), v)
		}
	}
	if got := logs[0].Header.Get("Accept"); got != "application/x-ndjson" {
		t.Errorf("records read Accept %q", got)
	}
	// The console takes no record filters (the API refuses them) and n is
	// capped at the largest size the page offers.
	q = logs[1].URL.Query()
	if q.Get("source") != "console" || q.Get("status") != "" || q.Get("n") != "500" {
		t.Errorf("console read: %s", logs[1].URL.RawQuery)
	}
}

func TestWorkloadLogsConsole(t *testing.T) {
	h := serve(t, populatedAPI())
	body := get(t, h, "/ui/tenants/tn_1/workloads/w/logs?source=console").Body.String()
	for _, want := range []string{"workerd starting", "red alert done", "[h1]", "did not answer"} {
		if !strings.Contains(body, want) {
			t.Errorf("console page lacks %q", want)
		}
	}
	if strings.Contains(body, "\x1b") || strings.Contains(body, "[31m") {
		t.Error("terminal escapes reached the page")
	}
}

func TestObjectLogs(t *testing.T) {
	api := populatedAPI()
	h := serve(t, api)
	for id, wantObj := range map[string]string{"obj_root": "obj_root", "obj_dead": "obj_dead"} { // obj_dead: killed (410)
		w := get(t, h, "/ui/objects/"+id+"/logs")
		if w.Code != 200 {
			t.Fatalf("%s: %d\n%s", id, w.Code, w.Body.String())
		}
		body := w.Body.String()
		if !strings.Contains(body, `href="/ui/objects/`+id+`"`) || !strings.Contains(body, `href="/ui/tenants/tn_1/workloads/w/logs?source=console"`) {
			t.Errorf("%s: node page lacks its links", id)
		}
		api.mu.Lock()
		var q string
		for _, r := range api.reqs {
			if strings.HasSuffix(r.URL.Path, "/logs") {
				q = r.URL.Query().Get("object")
			}
		}
		api.mu.Unlock()
		if q != wantObj {
			t.Errorf("%s: read records of object %q", id, q)
		}
	}
	// A node page does not let ?object= or ?source=console widen it.
	get(t, h, "/ui/objects/obj_root/logs?object=obj_kid&source=console")
	api.mu.Lock()
	r := api.reqs[len(api.reqs)-1]
	for i := len(api.reqs) - 1; i >= 0; i-- {
		if strings.HasSuffix(api.reqs[i].URL.Path, "/logs") {
			r = api.reqs[i]
			break
		}
	}
	api.mu.Unlock()
	if q := r.URL.Query(); q.Get("object") != "obj_root" || q.Get("source") != "requests" {
		t.Errorf("node page read %s", r.URL.RawQuery)
	}
	// The object and deployment pages link to the logs.
	if !strings.Contains(get(t, h, "/ui/objects/obj_root").Body.String(), `href="/ui/objects/obj_root/logs"`) {
		t.Error("object page lacks its logs link")
	}
	if !strings.Contains(get(t, h, "/ui/deployments/dep_1").Body.String(), `href="/ui/tenants/tn_1/workloads/w/logs"`) {
		t.Error("deployment page lacks its logs link")
	}
}

func TestWorkloadLogsUnavailable(t *testing.T) {
	for name, tc := range map[string]struct {
		err  *apiError
		want string
	}{
		"no log yet":   {&apiError{Status: 404, Code: "no_logs", Message: "host h1 has no log for workload w yet"}, "has no log for workload w yet"},
		"not running":  {&apiError{Status: 404, Code: "unknown_workload", Message: `tenant tn_1 has no running workload "w"`}, "has no running workload"},
		"host is down": {&apiError{Status: 502, Code: "host_unavailable", Message: "host h1 (holding workload w) did not answer"}, "host_unavailable: host h1"},
	} {
		api := populatedAPI()
		api.routes["GET /v1/tenants/tn_1/workloads/w/logs"] = tc.err
		w := get(t, serve(t, api), "/ui/tenants/tn_1/workloads/w/logs")
		if body := w.Body.String(); w.Code != 200 || !strings.Contains(body, tc.want) {
			t.Errorf("%s: %d, want the page with %q\n%s", name, w.Code, tc.want, body)
		}
	}
}

func TestLogsHostileOutputEscaped(t *testing.T) {
	const evil = `"><script>alert(1)</script><img src=x onerror=alert(2)>`
	api := populatedAPI()
	api.routes["GET /v1/tenants/tn_1/workloads/w/logs"] = rawBody(func(r *http.Request) (string, string) {
		if r.URL.Query().Get("source") == "console" {
			return "text/plain", evil + "\n"
		}
		b, _ := json.Marshal(map[string]any{"time": time.Now().UTC(), "kind": "request", "object": `x"` + evil, "class": evil, "method": "GET", "path": "/" + evil, "status": 200,
			"traceId": evil, "auditTraceId": evil, "caller": map[string]any{"user": evil},
			"logs": []any{map[string]any{"level": evil, "message": evil}}, "exceptions": []any{map[string]any{"name": evil, "message": evil, "stack": evil}}})
		return "application/x-ndjson", string(b) + "\n"
	})
	h := serve(t, api)
	for _, p := range []string{"/ui/tenants/tn_1/workloads/w/logs", "/ui/tenants/tn_1/workloads/w/logs?source=console", "/ui/objects/obj_root/logs"} {
		body := get(t, h, p).Body.String()
		for _, bad := range []string{"<script>alert", "<img src=x", `"><script`} {
			if strings.Contains(body, bad) {
				t.Errorf("%s: hostile markup %q rendered unescaped", p, bad)
			}
		}
		if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
			t.Errorf("%s: the hostile string is not shown escaped", p)
		}
	}
}

func TestParseReplicaPrefix(t *testing.T) {
	for line, want := range map[string][2]string{
		"[h1] hello":            {"h1", "hello"},
		"plain line":            {"", "plain line"},
		"[not a host] x":        {"", "[not a host] x"},
		"[] x":                  {"", "[] x"},
		"[INFO] workerd: ready": {"INFO", "workerd: ready"}, // ambiguous: shown as a replica tag, text intact
	} {
		if h, r := splitReplica(line); h != want[0] || r != want[1] {
			t.Errorf("%q: (%q, %q), want %v", line, h, r, want)
		}
	}
}
