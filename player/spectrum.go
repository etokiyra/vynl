package player

import "math"

const (
	// SpectrumBands is the number of frequency bands surfaced in Status.
	SpectrumBands = 48
	// analyzerSize is the number of output samples the FFT window covers.
	analyzerSize = 1024
)

// spectrumAnalyzer turns a window of recent output samples into log-spaced
// frequency-band levels in [0, 1]. It owns its scratch buffers so analysis
// allocates nothing after construction.
type spectrumAnalyzer struct {
	size   int
	window []float64
	re, im []float64
	edges  [SpectrumBands + 1]int
	ref    float64
}

func newSpectrumAnalyzer(size int, rate float64) *spectrumAnalyzer {
	analyzer := &spectrumAnalyzer{
		size:   size,
		window: make([]float64, size),
		re:     make([]float64, size),
		im:     make([]float64, size),
	}
	for i := range analyzer.window {
		analyzer.window[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(size-1))
	}
	// Peak DFT magnitude of a Hann-windowed full-scale sine is size/4.
	analyzer.ref = float64(size) / 4
	analyzer.setEdges(rate)
	return analyzer
}

// setEdges maps the log-spaced band boundaries onto FFT bins, clamping so the
// edges are monotonic and stay inside the usable spectrum.
func (a *spectrumAnalyzer) setEdges(rate float64) {
	binHz := rate / float64(a.size)
	maxFreq := math.Min(16000, rate/2)
	minFreq := 40.0
	if maxFreq <= minFreq {
		minFreq, maxFreq = 20, rate/2
	}
	previous := 1
	for band := 0; band <= SpectrumBands; band++ {
		frequency := minFreq * math.Pow(maxFreq/minFreq, float64(band)/SpectrumBands)
		edge := int(math.Round(frequency / binHz))
		edge = max(1, min(a.size/2-1, edge))
		edge = max(previous, edge)
		a.edges[band] = edge
		previous = edge
	}
}

// analyze windows and FFTs samples, writing one level per band. Samples longer
// than the window are truncated; shorter input is zero-padded.
func (a *spectrumAnalyzer) analyze(samples []float32, bands *[SpectrumBands]float64) {
	n := min(len(samples), a.size)
	for i := 0; i < n; i++ {
		a.re[i] = float64(samples[i]) * a.window[i]
		a.im[i] = 0
	}
	for i := n; i < a.size; i++ {
		a.re[i] = 0
		a.im[i] = 0
	}
	fftInPlace(a.re, a.im)

	lastBin := a.size/2 - 1
	for band := 0; band < SpectrumBands; band++ {
		lo, hi := a.edges[band], a.edges[band+1]
		if hi <= lo {
			hi = lo + 1
		}
		peak := 0.0
		for bin := lo; bin < hi && bin <= lastBin; bin++ {
			magnitude := math.Hypot(a.re[bin], a.im[bin])
			if magnitude > peak {
				peak = magnitude
			}
		}
		db := -120.0
		if peak > 0 {
			db = 20 * math.Log10(peak/a.ref)
		}
		bands[band] = clamp((db+60)/60, 0, 1)
	}
}

// fftInPlace is an iterative radix-2 Cooley-Tukey FFT. len(re) and len(im)
// must be equal powers of two.
func fftInPlace(re, im []float64) {
	n := len(re)
	if n < 2 || n&(n-1) != 0 {
		return
	}
	// Bit-reversal permutation.
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			re[i], re[j] = re[j], re[i]
			im[i], im[j] = im[j], im[i]
		}
	}
	for length := 2; length <= n; length <<= 1 {
		angle := -2 * math.Pi / float64(length)
		stepRe, stepIm := math.Cos(angle), math.Sin(angle)
		for start := 0; start < n; start += length {
			curRe, curIm := 1.0, 0.0
			for offset := 0; offset < length/2; offset++ {
				even, odd := start+offset, start+offset+length/2
				oddRe := re[odd]*curRe - im[odd]*curIm
				oddIm := re[odd]*curIm + im[odd]*curRe
				re[odd] = re[even] - oddRe
				im[odd] = im[even] - oddIm
				re[even] += oddRe
				im[even] += oddIm
				curRe, curIm = curRe*stepRe-curIm*stepIm, curRe*stepIm+curIm*stepRe
			}
		}
	}
}
