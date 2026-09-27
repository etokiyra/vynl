package player

import "testing"

func TestTransportPitchShiftsFrequencyIndependently(t *testing.T) {
	source := testTone(12000)
	streamer := newTransportStreamer(source, 12000)
	streamer.Configure(1, 2)
	output := make([]sample, 1024)
	if n, _ := streamer.Stream(output); n != len(output) {
		t.Fatalf("streamed %d samples, want %d", n, len(output))
	}

	crossings := 0
	for i := 1; i < len(output); i++ {
		if output[i-1][0] <= 0 && output[i][0] > 0 {
			crossings++
		}
	}
	if crossings < 32 || crossings > 44 {
		t.Fatalf("positive zero crossings = %d, want about twice the source frequency", crossings)
	}
}
