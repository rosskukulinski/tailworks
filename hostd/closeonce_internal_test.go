package hostd

import (
	"sync"
	"testing"
)

// TestCloseOnceConcurrent: a kill and a node join may close the same
// readyCh at the same time; neither may panic.
func TestCloseOnceConcurrent(t *testing.T) {
	for range 2000 {
		ch := make(chan struct{})
		var wg sync.WaitGroup
		start := make(chan struct{})
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				closeOnce(ch)
			}()
		}
		close(start)
		wg.Wait()
	}
}
