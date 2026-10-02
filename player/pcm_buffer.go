package player

import "sync"

type pcmBuffer struct {
	mu          sync.RWMutex
	samples     []pcmSample
	totalFrames int
	done        bool
	err         error
}

func newPCMBuffer(totalFrames int) *pcmBuffer {
	return &pcmBuffer{totalFrames: totalFrames}
}

func newPCMBufferWithInitial(totalFrames int, initial []pcmSample) *pcmBuffer {
	// The container may not report a usable length (some streams report 0).
	// Never advertise fewer frames than are already buffered, otherwise the
	// transport considers the track finished before it starts.
	if totalFrames < len(initial) {
		totalFrames = len(initial)
	}
	return &pcmBuffer{samples: initial, totalFrames: totalFrames}
}

func newCompletePCMBuffer(samples []pcmSample) *pcmBuffer {
	buffer := newPCMBuffer(len(samples))
	buffer.samples = samples
	buffer.done = true
	return buffer
}

func (b *pcmBuffer) append(samples []pcmSample) bool {
	if len(samples) == 0 {
		return true
	}
	// The capacity check and the slice update must happen under the same lock:
	// if they are split, two writers can both read the same slice header and
	// the later one overwrites samples appended by the earlier one.
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return false
	}
	if cap(b.samples)-len(b.samples) < len(samples) {
		capacity := maxInt(len(b.samples)+len(samples), maxInt(4096, cap(b.samples)*2))
		grown := make([]pcmSample, len(b.samples), capacity)
		copy(grown, b.samples)
		b.samples = grown
	}
	b.samples = append(b.samples, samples...)
	return true
}

func (b *pcmBuffer) snapshot() ([]pcmSample, int, bool, error) {
	b.mu.RLock()
	samples, totalFrames, done, err := b.samples, b.totalFrames, b.done, b.err
	b.mu.RUnlock()
	return samples, totalFrames, done, err
}

func (b *pcmBuffer) length() int {
	b.mu.RLock()
	length := len(b.samples)
	b.mu.RUnlock()
	return length
}

func (b *pcmBuffer) finish(err error) {
	b.mu.Lock()
	b.done = true
	b.err = err
	if len(b.samples) > 0 {
		b.totalFrames = len(b.samples)
	}
	b.mu.Unlock()
}

func (b *pcmBuffer) totalLength() int {
	b.mu.RLock()
	totalFrames := b.totalFrames
	b.mu.RUnlock()
	return totalFrames
}

func maxInt(first, second int) int {
	if first > second {
		return first
	}
	return second
}
