// Package queuecfg parses and validates a Worker's Cloudflare Queues
// configuration (wrangler.jsonc "queues": {"producers": [...],
// "consumers": [...]}) and holds the limits shared by hostd, mgmt and the
// CLI (ADR-039). Both hostd's bundle parser and mgmt's deploy check use
// it, so a bad config fails `tw deploy` with 400 bad_bundle.
package queuecfg

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Limits (Cloudflare's, except MaxConcurrency, which is tailworks').
const (
	MaxMessageBytes     = 128 * 1000 // one message body
	MaxBatchMessages    = 100        // one sendBatch, one pull
	MaxBatchBytes       = 256 * 1000 // one sendBatch
	MaxDelaySeconds     = 43200      // delaySeconds, delivery_delay, retry_delay
	MaxBatchSize        = 100        // max_batch_size
	MaxBatchTimeoutSecs = 60         // max_batch_timeout
	MaxRetries          = 100        // max_retries
	MaxConcurrency      = 20         // max_concurrency
	MaxVisibilityMillis = 12 * 3600 * 1000

	DefaultBatchSize        = 10
	DefaultBatchTimeoutSecs = 5
	DefaultMaxRetries       = 3
	DefaultConcurrency      = 1
)

// ContentTypes are the message content types (Cloudflare's).
var ContentTypes = []string{"json", "text", "bytes", "v8"}

// ValidContentType reports whether ct is a known content type.
func ValidContentType(ct string) bool {
	for _, c := range ContentTypes {
		if c == ct {
			return true
		}
	}
	return false
}

// NameRE is a valid queue name: Cloudflare's (lowercase letters, digits and
// dashes, at most 63, not starting or ending with a dash).
var NameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

var identRE = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// ValidName reports whether s is a valid queue name.
func ValidName(s string) bool { return NameRE.MatchString(s) }

// Producer is one producer binding: env.<Binding> sends to Queue.
type Producer struct {
	Binding string `json:"binding"`
	Queue   string `json:"queue"`
	// DeliveryDelay (seconds) delays every message sent through the
	// binding that doesn't give its own delaySeconds.
	DeliveryDelay int `json:"delivery_delay,omitempty"`
}

// Consumer is one consumer: the Worker's queue() handler gets Queue's
// messages (Type "worker"), or Queue is consumed by HTTP pull clients
// (Type "http_pull"; nothing runs on the host).
type Consumer struct {
	Queue           string `json:"queue"`
	Type            string `json:"type,omitempty"`
	MaxBatchSize    int    `json:"max_batch_size,omitempty"`
	MaxBatchTimeout *int   `json:"max_batch_timeout,omitempty"` // seconds
	MaxRetries      *int   `json:"max_retries,omitempty"`
	DeadLetterQueue string `json:"dead_letter_queue,omitempty"`
	RetryDelay      int    `json:"retry_delay,omitempty"` // seconds
	MaxConcurrency  int    `json:"max_concurrency,omitempty"`
}

// Push reports whether the consumer is a push (Worker) consumer.
func (c Consumer) Push() bool { return c.Type == "" || c.Type == "worker" }

// BatchSize, BatchTimeout, Retries and Concurrency are the effective
// settings (defaults applied).
func (c Consumer) BatchSize() int {
	if c.MaxBatchSize <= 0 {
		return DefaultBatchSize
	}
	return c.MaxBatchSize
}

func (c Consumer) BatchTimeout() time.Duration {
	if c.MaxBatchTimeout == nil {
		return DefaultBatchTimeoutSecs * time.Second
	}
	return time.Duration(*c.MaxBatchTimeout) * time.Second
}

func (c Consumer) Retries() int {
	if c.MaxRetries == nil {
		return DefaultMaxRetries
	}
	return *c.MaxRetries
}

func (c Consumer) Concurrency() int {
	if c.MaxConcurrency <= 0 {
		return DefaultConcurrency
	}
	return c.MaxConcurrency
}

// Config is wrangler.jsonc "queues".
type Config struct {
	Producers []Producer `json:"producers,omitempty"`
	Consumers []Consumer `json:"consumers,omitempty"`
}

// Empty reports whether there is nothing configured.
func (c *Config) Empty() bool { return c == nil || (len(c.Producers) == 0 && len(c.Consumers) == 0) }

// ProducerOf returns the producer binding of queue q (or nil).
func (c *Config) ProducerOf(q string) *Producer {
	if c == nil {
		return nil
	}
	for i := range c.Producers {
		if c.Producers[i].Queue == q {
			return &c.Producers[i]
		}
	}
	return nil
}

// ProducerBinding returns the producer binding named b (or nil).
func (c *Config) ProducerBinding(b string) *Producer {
	if c == nil {
		return nil
	}
	for i := range c.Producers {
		if c.Producers[i].Binding == b {
			return &c.Producers[i]
		}
	}
	return nil
}

// ConsumerOf returns the consumer of queue q (or nil).
func (c *Config) ConsumerOf(q string) *Consumer {
	if c == nil {
		return nil
	}
	for i := range c.Consumers {
		if c.Consumers[i].Queue == q {
			return &c.Consumers[i]
		}
	}
	return nil
}

// PushConsumers are the consumers hostd runs (Type "worker").
func (c *Config) PushConsumers() []Consumer {
	if c == nil {
		return nil
	}
	var out []Consumer
	for _, k := range c.Consumers {
		if k.Push() {
			out = append(out, k)
		}
	}
	return out
}

// Parse reads wrangler.jsonc's "queues" value (raw may be empty: no
// queues) and validates it.
func Parse(raw json.RawMessage) (*Config, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("queues: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Empty() {
		return nil, nil
	}
	return &c, nil
}

// FromWrangler reads "queues" from a whole (comment-stripped) wrangler
// JSON document.
func FromWrangler(wranglerJSON []byte) (*Config, error) {
	var w struct {
		Queues json.RawMessage `json:"queues"`
	}
	if err := json.Unmarshal(wranglerJSON, &w); err != nil {
		return nil, fmt.Errorf("wrangler.jsonc: %w", err)
	}
	return Parse(w.Queues)
}

func delayOK(n int) bool { return n >= 0 && n <= MaxDelaySeconds }

// Validate checks names, limits and duplicates.
func (c *Config) Validate() error {
	binds := map[string]bool{}
	for i, p := range c.Producers {
		where := fmt.Sprintf("queues.producers[%d]", i)
		if !identRE.MatchString(p.Binding) || len(p.Binding) > 128 || strings.HasPrefix(p.Binding, "__TW") {
			return fmt.Errorf("%s: bad binding name %q", where, p.Binding)
		}
		if binds[p.Binding] {
			return fmt.Errorf("%s: binding %s is declared twice", where, p.Binding)
		}
		binds[p.Binding] = true
		if !ValidName(p.Queue) {
			return fmt.Errorf("%s: bad queue name %q (lowercase letters, digits and dashes, at most 63)", where, p.Queue)
		}
		if !delayOK(p.DeliveryDelay) {
			return fmt.Errorf("%s: delivery_delay must be 0 to %d seconds", where, MaxDelaySeconds)
		}
	}
	consumed := map[string]bool{}
	for i, k := range c.Consumers {
		where := fmt.Sprintf("queues.consumers[%d]", i)
		if !ValidName(k.Queue) {
			return fmt.Errorf("%s: bad queue name %q (lowercase letters, digits and dashes, at most 63)", where, k.Queue)
		}
		if consumed[k.Queue] {
			return fmt.Errorf("%s: queue %s has two consumers in this Worker", where, k.Queue)
		}
		consumed[k.Queue] = true
		switch k.Type {
		case "", "worker", "http_pull":
		default:
			return fmt.Errorf("%s: type must be \"worker\" or \"http_pull\" (got %q)", where, k.Type)
		}
		if k.MaxBatchSize < 0 || k.MaxBatchSize > MaxBatchSize {
			return fmt.Errorf("%s: max_batch_size must be 1 to %d", where, MaxBatchSize)
		}
		if k.MaxBatchTimeout != nil && (*k.MaxBatchTimeout < 0 || *k.MaxBatchTimeout > MaxBatchTimeoutSecs) {
			return fmt.Errorf("%s: max_batch_timeout must be 0 to %d seconds", where, MaxBatchTimeoutSecs)
		}
		if k.MaxRetries != nil && (*k.MaxRetries < 0 || *k.MaxRetries > MaxRetries) {
			return fmt.Errorf("%s: max_retries must be 0 to %d", where, MaxRetries)
		}
		if !delayOK(k.RetryDelay) {
			return fmt.Errorf("%s: retry_delay must be 0 to %d seconds", where, MaxDelaySeconds)
		}
		if k.MaxConcurrency < 0 || k.MaxConcurrency > MaxConcurrency {
			return fmt.Errorf("%s: max_concurrency must be 1 to %d on tailworks", where, MaxConcurrency)
		}
		if k.DeadLetterQueue != "" {
			if !ValidName(k.DeadLetterQueue) {
				return fmt.Errorf("%s: bad dead_letter_queue name %q", where, k.DeadLetterQueue)
			}
			if k.DeadLetterQueue == k.Queue {
				return fmt.Errorf("%s: a queue can't be its own dead_letter_queue", where)
			}
		}
	}
	return nil
}
