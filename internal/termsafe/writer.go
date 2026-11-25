package termsafe

import (
	"bytes"
	"io"
	"sync"
)

// Writer passes a stream of log lines to w with each line escaped by Line:
// LF still ends a line (and a CR before it is dropped), every other
// control character is shown, never sent. A line is written once it is
// complete; Flush writes a trailing partial one.
type Writer struct {
	mu  sync.Mutex
	w   io.Writer
	buf []byte
}

// NewWriter returns a Writer to w.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

func (l *Writer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimSuffix(l.buf[:i], []byte("\r"))
		if _, err := io.WriteString(l.w, Line(string(line))+"\n"); err != nil {
			return 0, err
		}
		l.buf = l.buf[i+1:]
	}
	return len(p), nil
}

// Flush writes a partial last line, if any (with a newline).
func (l *Writer) Flush() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buf) == 0 {
		return nil
	}
	_, err := io.WriteString(l.w, Line(string(l.buf))+"\n")
	l.buf = nil
	return err
}
