package player

import (
	"math"
	"testing"
)

func TestFFTResolvesASingleFrequency(t *testing.T) {
	const n = 64
	re := make([]float64, n)
	im := make([]float64, n)
	for i := range re {
		re[i] = math.Cos(2 * math.Pi * 16 * float64(i) / n)
	}
	fftInPlace(re, im)

	peak := 16
	if got := math.Hypot(re[peak], im[peak]); math.Abs(got-n/2) > 1 {
		t.Fatalf("bin %d magnitude = %.3f, want about %d", peak, got, n/2)
	}
	for bin := 1; bin < n/2; bin++ {
		if bin == peak {
			continue
		}
		if got := math.Hypot(re[bin], im[bin]); got > 1 {
			t.Fatalf("unexpected energy %.3f at bin %d", got, bin)
		}
	}
}

func TestSpectrumAnalyzerSilenceIsZero(t *testing.T) {
	analyzer := newSpectrumAnalyzer(analyzerSize, outputRate)
	var bands [SpectrumBands]float64
	analyzer.analyze(make([]float32, analyzerSize), &bands)
	for band, level := range bands {
		if level != 0 {
			t.Fatalf("band %d of silence = %.3f, want 0", band, level)
		}
	}
}

func TestSpectrumAnalyzerLocalizesFrequency(t *testing.T) {
	analyzer := newSpectrumAnalyzer(analyzerSize, outputRate)
	peakBand := func(frequency float64) (int, float64) {
		samples := make([]float32, analyzerSize)
		for i := range samples {
			samples[i] = float32(math.Sin(2 * math.Pi * frequency * float64(i) / outputRate))
		}
		var bands [SpectrumBands]float64
		analyzer.analyze(samples, &bands)
		best, bestLevel := 0, 0.0
		for band, level := range bands {
			if level > bestLevel {
				best, bestLevel = band, level
			}
		}
		return best, bestLevel
	}

	lowBand, lowLevel := peakBand(1000)
	highBand, _ := peakBand(8000)
	if lowLevel < 0.75 {
		t.Fatalf("full-scale tone peak level = %.3f, want near 1", lowLevel)
	}
	if highBand <= lowBand {
		t.Fatalf("8 kHz peaked at band %d, 1 kHz at band %d; expected a higher band", highBand, lowBand)
	}
	if lowBand < 15 || lowBand > 35 {
		t.Fatalf("1 kHz peaked at band %d, outside the expected midrange", lowBand)
	}
}

func TestSpectrumAnalyzerRollsOffDistantBands(t *testing.T) {
	analyzer := newSpectrumAnalyzer(analyzerSize, outputRate)
	samples := make([]float32, analyzerSize)
	for i := range samples {
		samples[i] = float32(math.Sin(2 * math.Pi * 1000 * float64(i) / outputRate))
	}
	var bands [SpectrumBands]float64
	analyzer.analyze(samples, &bands)

	peak := 0
	for band, level := range bands {
		if level > bands[peak] {
			peak = band
		}
	}
	far := peak + 12
	if far >= SpectrumBands {
		far = peak - 12
	}
	if bands[far] >= bands[peak]/2 {
		t.Fatalf("tone did not roll off: peak band %.3f, far band %.3f", bands[peak], bands[far])
	}
}
