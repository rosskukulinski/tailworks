package queuecfg

import (
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	c, err := FromWrangler([]byte(`{"name":"x","queues":{
		"producers":[{"binding":"JOBS","queue":"jobs"},{"binding":"LATER","queue":"later","delivery_delay":30}],
		"consumers":[{"queue":"jobs","max_batch_size":5,"max_batch_timeout":0,"max_retries":0,"dead_letter_queue":"jobs-dlq","retry_delay":2},
		             {"queue":"pulled","type":"http_pull"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.ProducerOf("later").DeliveryDelay != 30 || c.ProducerOf("nope") != nil {
		t.Fatalf("producers: %+v", c.Producers)
	}
	k := c.ConsumerOf("jobs")
	if k.BatchSize() != 5 || k.BatchTimeout() != 0 || k.Retries() != 0 || k.Concurrency() != 1 || k.DeadLetterQueue != "jobs-dlq" {
		t.Fatalf("consumer: %+v", k)
	}
	if p := c.PushConsumers(); len(p) != 1 || p[0].Queue != "jobs" {
		t.Fatalf("push consumers: %+v", p)
	}
	d := Consumer{Queue: "q"}
	if d.BatchSize() != 10 || d.BatchTimeout() != 5*time.Second || d.Retries() != 3 {
		t.Fatalf("defaults: %d %v %d", d.BatchSize(), d.BatchTimeout(), d.Retries())
	}
	if c, err := FromWrangler([]byte(`{"name":"x"}`)); err != nil || c != nil {
		t.Fatalf("no queues: %v %v", c, err)
	}
	if c, err := Parse([]byte(`{"producers":[]}`)); err != nil || c != nil {
		t.Fatalf("empty queues: %v %v", c, err)
	}
}

func TestParseRejects(t *testing.T) {
	for _, tc := range []struct{ cfg, want string }{
		{`{"producers":[{"binding":"Q","queue":"Bad_Name"}]}`, "bad queue name"},
		{`{"producers":[{"binding":"Q","queue":"-x"}]}`, "bad queue name"},
		{`{"producers":[{"binding":"1Q","queue":"q"}]}`, "bad binding name"},
		{`{"producers":[{"binding":"__TW_Q","queue":"q"}]}`, "bad binding name"},
		{`{"producers":[{"binding":"Q","queue":"q"},{"binding":"Q","queue":"r"}]}`, "declared twice"},
		{`{"producers":[{"binding":"Q","queue":"q","delivery_delay":43201}]}`, "delivery_delay"},
		{`{"consumers":[{"queue":"q"},{"queue":"q"}]}`, "two consumers"},
		{`{"consumers":[{"queue":"q","max_batch_size":101}]}`, "max_batch_size"},
		{`{"consumers":[{"queue":"q","max_batch_timeout":61}]}`, "max_batch_timeout"},
		{`{"consumers":[{"queue":"q","max_retries":101}]}`, "max_retries"},
		{`{"consumers":[{"queue":"q","retry_delay":-1}]}`, "retry_delay"},
		{`{"consumers":[{"queue":"q","max_concurrency":21}]}`, "max_concurrency"},
		{`{"consumers":[{"queue":"q","dead_letter_queue":"q"}]}`, "its own"},
		{`{"consumers":[{"queue":"q","type":"push"}]}`, "type must be"},
		{`{"consumers":"q"}`, "queues:"},
	} {
		_, err := Parse([]byte(tc.cfg))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.cfg, err, tc.want)
		}
	}
	for _, ok := range []string{"a", "jobs", "my-queue-1", strings.Repeat("a", 63)} {
		if !ValidName(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "A", "a_b", "a-", strings.Repeat("a", 64)} {
		if ValidName(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}
