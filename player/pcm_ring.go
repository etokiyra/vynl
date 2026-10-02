package player

import "sync"

// pcmRing is a fixed-capacity, single-producer/single-consumer store of decoded
// frames at the output sample rate. It replaces the old unbounded pcmBuffer:
// memory use is bounded by the constructor capacity no matter how long the
// track is.
//
// The producer is the background decoder goroutine; the consumer is the
// real-time transportStreamer.Stream callback. All ring state is guarded by a
// single mutex; a producer that cannot make room blocks (outside the mutex) and
// is woken by the consumer via the buffered wake channel.
//
// Absolute frame coordinates are used throughout, matching the transport's
// index domain. The resident window is [base, base+length); readHead is the
// consumer's current playback frame.
type pcmRing struct {
	mu       sync.Mutex
	samples  []pcmSample
	capacity int
	lookback int // policy: frames retained behind readHead (see newPCMRingWith)
	ahead    int // policy/requirement: frames the transport may read ahead
	start    int // ring slot holding absolute frame base
	base     int64
	length   int
	readHead int64
	total    int64
	finished bool
	err      error
	static   bool // complete, already-decoded source; never re-seeks

	// Seek protocol. seekGen is bumped by the consumer to request a decoder
	// seek; seekAcked is set by the producer once the ring has been reset to
	// the requested frame. The consumer refuses to read while
	// seekAcked < seekGen (see window).
	seekGen   uint64
	seekAcked uint64
	seekFrame int64

	wake chan struct{}
}

// ringView is the consumer's view of a contiguous source window. When ready is
// false the transport must take its existing buffering/underflow path.
type ringView struct {
	samples  []pcmSample
	base     int64
	total    int64
	finished bool
	ready    bool
}

const (
	// ringMargin is the transport's hard requirement: alignGrain searches ±160
	// frames around a grain start, so the producer must never overwrite the
	// 160 frames behind readHead that the transport may still read.
	ringMargin = 160
	// ringMaxPitch mirrors the transport's pitch clamp (2^2).
	ringMaxPitch = 4
	// ringAheadWindow is the transport's hard read-ahead requirement:
	// nominal + grainSize*pitch + margin, plus margin slack.
	ringAheadWindow = grainSize*ringMaxPitch + 2*ringMargin
	// ringChunkFrames is the decoder's write granularity (one decode chunk).
	ringChunkFrames = 4096

	// ringLookbackFrames is a *policy* knob, not a transport requirement. It
	// trades read-ahead for instant backward-seek responsiveness: a backward
	// seek within this many frames is served from the ring without touching the
	// decoder. It must stay strictly greater than ringMargin (correctness: the
	// producer's flow control only guarantees it will not overwrite
	// readHead-ringLookback, and the transport reads readHead-ringMargin).
	// A future reader may shrink it toward ringMargin+1 without breaking audio;
	// they must not shrink it to or below ringMargin.
	ringLookbackFrames = outputRate // 1.0 s
	// ringCapacityFrames trades RAM for seek responsiveness and decoder slack:
	// 4.0 s at 44100 Hz is ~1.35 MiB at 8 bytes/frame, constant for any file.
	ringCapacityFrames = 4 * outputRate
)

func newPCMRing(capacity int) *pcmRing {
	return newPCMRingWith(capacity, ringLookbackFrames, ringAheadWindow)
}

// newPCMRingWith is the testable constructor. Both bounds below exist for
// correctness, not tuning:
//   - capacity must hold the transport's read-ahead window plus the alignGrain
//     margin plus one producer chunk, or the producer could never make progress
//     (tryWrite would always fail).
//   - lookback must exceed ringMargin, or flow control could legally overwrite
//     frames that alignGrain is about to read.
func newPCMRingWith(capacity, lookback, ahead int) *pcmRing {
	if capacity < ahead+ringMargin+ringChunkFrames {
		panic("pcmRing capacity too small for read-ahead + margin + chunk")
	}
	if lookback <= ringMargin {
		panic("pcmRing lookback must exceed the alignGrain margin")
	}
	return &pcmRing{
		samples:  make([]pcmSample, capacity),
		capacity: capacity,
		lookback: lookback,
		ahead:    ahead,
		wake:     make(chan struct{}, 1),
	}
}

// newCompletePCMRing wraps an already-decoded slice as a finished, non-streaming
// ring. It is the test/whole-track path: no producer, no flow control, and the
// window is returned without copying.
func newCompletePCMRing(samples []pcmSample) *pcmRing {
	return &pcmRing{
		samples:  samples,
		capacity: len(samples),
		length:   len(samples),
		total:    int64(len(samples)),
		readHead: int64(len(samples)),
		finished: true,
		static:   true,
		wake:     make(chan struct{}, 1),
	}
}

// freeSpaceLocked reports how many frames the producer may append before it
// must wait. Droppable space is monotonic non-decreasing in readHead: moving
// readHead backward can only reduce it, so a backward seek can never trick the
// producer into overwriting data the consumer still needs. Must hold r.mu.
func (r *pcmRing) freeSpaceLocked() int {
	drop := r.readHead - int64(r.lookback) - r.base
	if drop < 0 {
		drop = 0
	}
	if drop > int64(r.length) {
		drop = int64(r.length)
	}
	return (r.capacity - r.length) + int(drop)
}

// freeSpace is the test-visible form of freeSpaceLocked.
func (r *pcmRing) freeSpace() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.freeSpaceLocked()
}

func (r *pcmRing) capacityFrames() int {
	return r.capacity
}

func (r *pcmRing) residentFrames() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.length
}

// notifyLocked wakes a waiting producer without blocking. It is only a hint:
// the producer re-reads all authoritative state under r.mu after waking, so a
// dropped notify (buffered channel already full) cannot lose an update.
func (r *pcmRing) notifyLocked() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *pcmRing) notify() {
	r.mu.Lock()
	r.notifyLocked()
	r.mu.Unlock()
}

func (r *pcmRing) dropLocked(frames int) {
	if frames <= 0 {
		return
	}
	if frames > r.length {
		frames = r.length
	}
	r.start = (r.start + frames) % r.capacity
	r.base += int64(frames)
	r.length -= frames
}

// appendLocked drops the oldest frames only as needed to fit chunk, then writes
// it, handling wrap. Must hold r.mu and be called only when freeSpaceLocked()
// already allows it.
func (r *pcmRing) appendLocked(chunk []pcmSample) {
	if r.length+len(chunk) > r.capacity {
		r.dropLocked(r.length + len(chunk) - r.capacity)
	}
	slot := (r.start + r.length) % r.capacity
	written := copy(r.samples[slot:], chunk)
	if written < len(chunk) {
		copy(r.samples[:], chunk[written:])
	}
	r.length += len(chunk)
}

// tryWrite appends one decoded chunk if there is room right now. The space
// check and the append happen in the same locked section: a backward readHead
// move between a separate check and write could otherwise shrink the allowed
// region and let the producer overwrite live data. Returns false when the
// caller must wait and retry.
func (r *pcmRing) tryWrite(chunk []pcmSample) bool {
	if len(chunk) == 0 {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return false
	}
	if len(chunk) > r.freeSpaceLocked() {
		return false
	}
	r.appendLocked(chunk)
	return true
}

// wait blocks until the consumer signals progress or cancel fires. It is used
// only after tryWrite returned false; the caller re-tries and re-checks for a
// pending seek after every wake.
func (r *pcmRing) wait(cancel <-chan struct{}) bool {
	select {
	case <-r.wake:
		return true
	case <-cancel:
		return false
	}
}

// pendingSeek reports whether the consumer has requested a decoder seek that
// has not been served yet.
func (r *pcmRing) pendingSeek() (gen uint64, frame int64, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seekGen == r.seekAcked {
		return 0, 0, false
	}
	return r.seekGen, r.seekFrame, true
}

// commitSeek resets the ring to frame after the decoder has been repositioned.
// If a newer seek raced in while the decoder was seeking, this result is
// discarded and no ack is published, so the consumer keeps waiting.
func (r *pcmRing) commitSeek(gen uint64, frame int64) {
	r.mu.Lock()
	if r.seekGen == gen {
		r.base = frame
		r.length = 0
		r.start = 0
		r.finished = false
		r.err = nil
		r.seekAcked = gen
	}
	r.mu.Unlock()
	r.notify()
}

// finish marks the ring complete and pins total to the decoded extent (which
// also makes unknown-length containers playable end to end).
func (r *pcmRing) finish(err error) {
	r.mu.Lock()
	if !r.finished {
		r.finished = true
		r.err = err
		r.total = r.base + int64(r.length)
	}
	r.mu.Unlock()
	r.notify()
}

// ensure publishes the consumer's new playback frame and requests a decoder
// seek only when target is not comfortably resident. Re-seek is consumer-side:
// the producer only obeys flow control and seek requests, it never decides to
// seek on its own.
func (r *pcmRing) ensure(target int64) {
	if r.static {
		r.mu.Lock()
		r.readHead = target
		r.mu.Unlock()
		return
	}
	r.mu.Lock()
	r.readHead = target
	end := r.base + int64(r.length)
	resident := target >= r.base && target+int64(r.ahead) <= end
	if !resident {
		r.seekFrame = target
		r.seekGen++
		r.mu.Unlock()
		r.notify()
		return
	}
	r.mu.Unlock()
	r.notify()
}

// window publishes readHead=lo and returns a contiguous view of [lo, hi)
// copied into dst. Static (complete) rings return a subslice of their backing
// array instead, so the whole-track path stays zero-copy.
//
// ready=false means the caller must take the buffering path. This happens in
// exactly two cases, and both route through the same silence+buffering=true
// handling in the transport:
//   - a seek rewrite is in flight (seekAcked < seekGen), or
//   - lo is older than the resident window (evicted; caller should ensure()).
//
// Ordering: the producer writes base/length/samples and then unlocks; the
// consumer's next Lock happens-after that release, so it observes the reset
// ring state before it can observe seekAcked == seekGen. That mutex
// release->acquire chain is the whole mechanism; no atomics are needed.
func (r *pcmRing) window(lo, hi int64, dst []pcmSample) ringView {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.static {
		end := r.base + int64(r.length)
		if lo < r.base {
			lo = r.base
		}
		if hi > end {
			hi = end
		}
		if hi <= lo {
			return ringView{total: r.total, finished: true}
		}
		return ringView{
			samples:  r.samples[int(lo-r.base):int(hi-r.base)],
			base:     lo,
			total:    r.total,
			finished: true,
			ready:    true,
		}
	}

	if r.seekAcked < r.seekGen {
		return ringView{total: r.total, finished: r.finished}
	}

	r.readHead = lo
	r.notifyLocked()

	end := r.base + int64(r.length)
	if lo < r.base || lo >= end {
		return ringView{total: r.total, finished: r.finished}
	}
	if hi > end {
		hi = end
	}
	if hi <= lo {
		return ringView{total: r.total, finished: r.finished}
	}
	n := int(hi - lo)
	r.copyTo(dst[:n], lo)
	return ringView{
		samples:  dst[:n],
		base:     lo,
		total:    r.total,
		finished: r.finished,
		ready:    true,
	}
}

// copyTo copies absolute frames [base, base+len(dst)) into dst, handling wrap.
func (r *pcmRing) copyTo(dst []pcmSample, base int64) {
	offset := int((base - r.base + int64(r.start)) % int64(r.capacity))
	n := copy(dst, r.samples[offset:])
	if n < len(dst) {
		copy(dst[n:], r.samples[:len(dst)-n])
	}
}

func (r *pcmRing) errValue() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// status returns whether decoding has finished and the known total length.
func (r *pcmRing) status() (finished bool, total int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finished, r.total
}

// setTotal records a length declared by the container header, so duration and
// end-of-track are known before the background decode fills the ring.
func (r *pcmRing) setTotal(total int64) {
	if total <= 0 {
		return
	}
	r.mu.Lock()
	r.total = total
	r.mu.Unlock()
}

// isFinished reports whether the producer has stopped writing (EOF, error, or
// abort). The producer stays alive after this so later seeks can resume it.
func (r *pcmRing) isFinished() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finished
}
