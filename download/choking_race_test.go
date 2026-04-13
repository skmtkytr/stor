package download

import (
	"sync"
	"testing"
)

// TestClientChokingConcurrentAccess verifies that concurrent reads/writes to
// the choking field do not cause a data race. Run with: go test -race
func TestClientChokingConcurrentAccess(t *testing.T) {
	c := &Client{
		choking: true,
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// Concurrent reader (simulating PeerManager calling IsChoking)
	go func() {
		defer wg.Done()
		for range 1000 {
			_ = c.IsChoking()
		}
	}()

	// Concurrent writer (simulating SendChoke/SendUnchoke path via stateMu)
	go func() {
		defer wg.Done()
		for range 1000 {
			c.stateMu.Lock()
			c.choking = !c.choking
			c.stateMu.Unlock()
		}
	}()

	wg.Wait()
}
