package player

import (
	"sync"
	"testing"
)

// TestPCMBufferConcurrentAppendsDoNotLoseSamples guards the buffer against the
// lost-update race where two writers read the same slice header before either
// installs its grown copy. With a chunk that always exceeds the remaining
// capacity, every append takes the growth path, maximizing the overlap window.
func TestPCMBufferConcurrentAppendsDoNotLoseSamples(t *testing.T) {
	const (
		writers  = 8
		appends  = 20
		chunk    = 4096
		expected = writers * appends * chunk
	)

	buffer := &pcmBuffer{}
	start := make(chan struct{})
	var group sync.WaitGroup
	for writer := 0; writer < writers; writer++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			for i := 0; i < appends; i++ {
				if !buffer.append(make([]pcmSample, chunk)) {
					t.Errorf("append rejected while buffer was open")
					return
				}
			}
		}()
	}
	close(start)
	group.Wait()

	if got := buffer.length(); got != expected {
		t.Fatalf("buffer length = %d, want %d (%d samples lost)", got, expected, expected-got)
	}
}
