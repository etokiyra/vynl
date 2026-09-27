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
	b.mu.RLock()
	current := b.samples
	done := b.done
	b.mu.RUnlock()
	if done {
		return false
	}

	if cap(current)-len(current) < len(samples) {
		capacity := maxInt(len(current)+len(samples), maxInt(4096, cap(current)*2))
		grown := make([]pcmSample, len(current), capacity)
		copy(grown, current)
		grown = append(grown, samples...)
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.done {
			return false
		}
		b.samples = grown
		return true
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return false
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
