package main

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// TestCLIQueues: create, send (JSON, text, stdin), ls with counters,
// messages (peek), info, purge and rm against a real mgmt; bad names and
// non-JSON bodies are refused before anything is sent.
func TestCLIQueues(t *testing.T) {
	e := newPubEnv(t)
	e.setPolicy(`{}`)
	if out, err := e.call(t, cmdQueues, "create", "jobs"); err != nil || !strings.Contains(out, "queue jobs") {
		t.Fatalf("create: %v\n%s", err, out)
	}
	if out, err := e.call(t, cmdQueues, "send", "jobs", `{"k":1}`); err != nil || !strings.Contains(out, "sent qm_") {
		t.Fatalf("send: %v\n%s", err, out)
	}
	if out, err := e.call(t, cmdQueues, "send", "jobs", "--text", "hello there"); err != nil {
		t.Fatalf("send --text: %v\n%s", err, out)
	}
	old := queueInput
	t.Cleanup(func() { queueInput = old })
	queueInput = func() io.Reader { return strings.NewReader(`{"from":"stdin"}`) }
	if out, err := e.call(t, cmdQueues, "send", "later", "--delay", "60"); err != nil {
		t.Fatalf("send from stdin: %v\n%s", err, out)
	}
	out, err := e.call(t, cmdQueues, "ls")
	if err != nil || !strings.Contains(out, "jobs") || !strings.Contains(out, "later") {
		t.Fatalf("ls: %v\n%s", err, out)
	}
	out, err = e.call(t, cmdQueues, "ls", "--json")
	var lr queuesResult
	if err != nil || json.Unmarshal([]byte(out), &lr) != nil || lr.Queues == nil || len(*lr.Queues) != 2 || (*lr.Queues)[0].Name != "jobs" || (*lr.Queues)[0].Backlog != 2 || (*lr.Queues)[1].Delayed != 1 {
		t.Fatalf("ls --json: %v\n%s", err, out)
	}
	out, err = e.call(t, cmdQueues, "messages", "jobs")
	if err != nil || !strings.Contains(out, `{"k":1}`) || !strings.Contains(out, "hello there") {
		t.Fatalf("messages: %v\n%s", err, out)
	}
	if out, err := e.call(t, cmdQueues, "info", "later"); err != nil || !strings.Contains(out, "1 delayed") {
		t.Fatalf("info: %v\n%s", err, out)
	}
	if out, err := e.call(t, cmdQueues, "purge", "jobs", "--yes"); err != nil || !strings.Contains(out, "purged 2 messages") {
		t.Fatalf("purge: %v\n%s", err, out)
	}
	if out, err := e.call(t, cmdQueues, "rm", "later", "--yes"); err != nil || !strings.Contains(out, "deleted") {
		t.Fatalf("rm: %v\n%s", err, out)
	}
	if _, err := e.call(t, cmdQueues, "info", "later"); cliCode(err) != "unknown_queue" {
		t.Fatalf("info after rm: %v", err)
	}
	for _, args := range [][]string{{"create", "Bad_Name"}, {"send", "jobs", "not json"}, {"info"}, {"frob", "jobs"}, {}} {
		if _, err := e.call(t, cmdQueues, args...); cliCode(err) != "bad_request" {
			t.Errorf("%v: %v", args, err)
		}
	}
}
