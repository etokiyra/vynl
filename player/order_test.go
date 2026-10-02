package player

import "testing"

func TestPlayOrderAdvancesAndWraps(t *testing.T) {
	order := newPlayOrder(4)
	if got := order.current(); got != 0 {
		t.Fatalf("initial current = %d, want 0", got)
	}

	next, wrapped := order.advance(1)
	if next != 1 || wrapped {
		t.Fatalf("advance(1) = (%d, %t), want (1, false)", next, wrapped)
	}
	order.setCurrent(3)
	next, wrapped = order.advance(1)
	if next != 0 || !wrapped {
		t.Fatalf("advance(1) from the end = (%d, %t), want (0, true)", next, wrapped)
	}
	next, wrapped = order.advance(-1)
	if next != 3 || !wrapped {
		t.Fatalf("advance(-1) from the start = (%d, %t), want (3, true)", next, wrapped)
	}
	if got := order.current(); got != 3 {
		t.Fatalf("current after advancing = %d, want 3", got)
	}
}

func TestPlayOrderShuffleKeepsCurrentAndStaysAPermutation(t *testing.T) {
	const count = 32
	order := newPlayOrder(count)
	order.setCurrent(17)
	order.shuffle()

	if got := order.current(); got != 17 {
		t.Fatalf("shuffle changed the current track to %d, want 17", got)
	}
	seen := make([]bool, count)
	for _, index := range order.indices {
		if index < 0 || index >= count {
			t.Fatalf("shuffled index out of range: %d", index)
		}
		if seen[index] {
			t.Fatalf("shuffled order contains duplicate index %d", index)
		}
		seen[index] = true
	}
	for index, present := range seen {
		if !present {
			t.Fatalf("shuffled order is missing index %d", index)
		}
	}
}

func TestPlayOrderResetReturnsToNaturalOrder(t *testing.T) {
	order := newPlayOrder(3)
	order.shuffle()
	order.setCurrent(1)
	order.reset(3)
	if got := order.current(); got != 1 {
		t.Fatalf("reset current = %d, want 1", got)
	}
	for i, index := range order.indices {
		if index != i {
			t.Fatalf("reset order[%d] = %d, want %d", i, index, i)
		}
	}
}

func TestRepeatAdvanceHonorsModes(t *testing.T) {
	// Mid-queue: every mode advances to the next track.
	order := newPlayOrder(3)
	if next, stop := repeatAdvance(&order, 0, RepeatOff, false); next != 1 || stop {
		t.Fatalf("mid-queue RepeatOff = (%d, %t), want (1, false)", next, stop)
	}

	// RepeatOne replays the current track without moving the order.
	order = newPlayOrder(3)
	order.setCurrent(1)
	if next, stop := repeatAdvance(&order, 1, RepeatOne, false); next != 1 || stop {
		t.Fatalf("RepeatOne = (%d, %t), want (1, false)", next, stop)
	}
	if got := order.current(); got != 1 {
		t.Fatalf("RepeatOne moved the order to %d, want 1", got)
	}

	// RepeatOff stops at the end without wrapping.
	order = newPlayOrder(3)
	order.setCurrent(2)
	if next, stop := repeatAdvance(&order, 2, RepeatOff, false); next != 2 || !stop {
		t.Fatalf("end-of-queue RepeatOff = (%d, %t), want (2, true)", next, stop)
	}

	// RepeatAll wraps to the start.
	order = newPlayOrder(3)
	order.setCurrent(2)
	if next, stop := repeatAdvance(&order, 2, RepeatAll, false); next != 0 || stop {
		t.Fatalf("end-of-queue RepeatAll = (%d, %t), want (0, false)", next, stop)
	}

	// Shuffle always loops, even with RepeatOff.
	order = newPlayOrder(3)
	order.setCurrent(2)
	if next, stop := repeatAdvance(&order, 2, RepeatOff, true); next != 0 || stop {
		t.Fatalf("end-of-queue shuffle = (%d, %t), want (0, false)", next, stop)
	}

	// A single-track order wraps on every step, so RepeatOff still stops it.
	single := newPlayOrder(1)
	if next, stop := repeatAdvance(&single, 0, RepeatOff, false); next != 0 || !stop {
		t.Fatalf("single-track RepeatOff = (%d, %t), want (0, true)", next, stop)
	}
	if next, stop := repeatAdvance(&single, 0, RepeatAll, false); next != 0 || stop {
		t.Fatalf("single-track RepeatAll = (%d, %t), want (0, false)", next, stop)
	}
}

func TestRepeatModeCycles(t *testing.T) {
	mode := RepeatOff
	want := []RepeatMode{RepeatAll, RepeatOne, RepeatOff, RepeatAll}
	for _, expected := range want {
		mode = mode.next()
		if mode != expected {
			t.Fatalf("next() = %q, want %q", mode, expected)
		}
	}
}
