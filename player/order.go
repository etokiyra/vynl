package player

import "math/rand/v2"

// RepeatMode controls what happens when the current track reaches its end.
type RepeatMode string

const (
	// RepeatOff stops playback once the end of the play order is reached.
	RepeatOff RepeatMode = "off"
	// RepeatAll wraps around to the start of the play order.
	RepeatAll RepeatMode = "all"
	// RepeatOne replays the current track.
	RepeatOne RepeatMode = "one"
)

func (r RepeatMode) next() RepeatMode {
	switch r {
	case RepeatOff:
		return RepeatAll
	case RepeatAll:
		return RepeatOne
	default:
		return RepeatOff
	}
}

// playOrder is the sequence of track indices the engine advances through.
//
// With shuffle off the order is the identity permutation, so Next/Prev wrap
// over the library in its natural order. With shuffle on it is a permutation;
// the currently selected track is kept in place so toggling shuffle does not
// interrupt playback.
type playOrder struct {
	indices []int
	pos     int
}

func newPlayOrder(count int) playOrder {
	return playOrder{indices: identityOrder(count)}
}

func identityOrder(count int) []int {
	indices := make([]int, count)
	for i := range indices {
		indices[i] = i
	}
	return indices
}

// reset rebuilds an identity order for count tracks, preserving the current
// track where it still fits.
func (o *playOrder) reset(count int) {
	current := o.current()
	o.indices = identityOrder(count)
	o.pos = 0
	if current >= 0 && current < count {
		o.pos = current
	}
}

// shuffle randomizes the order in place while keeping the current track at the
// current position.
func (o *playOrder) shuffle() {
	if len(o.indices) < 2 {
		return
	}
	current := o.current()
	rand.Shuffle(len(o.indices), func(i, j int) {
		o.indices[i], o.indices[j] = o.indices[j], o.indices[i]
	})
	o.pos = o.indexOf(current)
}

func (o *playOrder) current() int {
	if len(o.indices) == 0 {
		return 0
	}
	return o.indices[o.pos]
}

// setCurrent moves the play position to wherever index sits in the order.
func (o *playOrder) setCurrent(index int) {
	if position := o.indexOf(index); position >= 0 {
		o.pos = position
	}
}

func (o *playOrder) indexOf(index int) int {
	for position, value := range o.indices {
		if value == index {
			return position
		}
	}
	return -1
}

// peek reports the index delta steps away without moving the play position.
// wrapped is true when the step crossed the end (or start) of the order.
func (o *playOrder) peek(delta int) (index int, wrapped bool) {
	count := len(o.indices)
	if count == 0 {
		return 0, false
	}
	next := (o.pos + delta) % count
	if next < 0 {
		next += count
	}
	switch {
	case delta > 0:
		wrapped = o.pos+delta >= count
	case delta < 0:
		wrapped = o.pos+delta < 0
	}
	return o.indices[next], wrapped
}

// advance moves delta steps through the order and commits the new position.
func (o *playOrder) advance(delta int) (index int, wrapped bool) {
	index, wrapped = o.peek(delta)
	count := len(o.indices)
	if count == 0 {
		return index, wrapped
	}
	next := (o.pos + delta) % count
	if next < 0 {
		next += count
	}
	o.pos = next
	return index, wrapped
}

// repeatAdvance decides which track should play when current finishes, and
// whether playback should stop at the end of the order. RepeatOne replays
// current; RepeatOff stops after the last entry unless shuffle is active
// (shuffle always loops).
func repeatAdvance(order *playOrder, current int, repeat RepeatMode, shuffle bool) (index int, stop bool) {
	if repeat == RepeatOne || len(order.indices) == 0 {
		return current, false
	}
	next, wrapped := order.peek(1)
	if wrapped && repeat == RepeatOff && !shuffle {
		return current, true
	}
	order.advance(1)
	return next, false
}
