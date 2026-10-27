package main

import (
	"math"
	"sort"
)

// Waveform display parameters (match the Python audio worker).
const (
	waveformBars     = 20
	peakWindowSize   = 90 // ~3s rolling window at 30fps
	waveformInterval = 33 // milliseconds, ~30fps
)

// waveformAgg turns raw sample chunks into normalized bar heights using the
// same auto-normalization as the Python version: 85th-percentile of a rolling
// peak window, smoothed with an EMA.
type waveformAgg struct {
	bars        int
	peakHistory []float64
	peakLevel   float64
}

func newWaveformAgg(bars int) *waveformAgg {
	return &waveformAgg{bars: bars, peakLevel: 0.01}
}

// update consumes a chunk of samples and returns bar values in [0,1] plus the
// raw (unnormalized) peak RMS, comparable against silenceThreshold.
func (w *waveformAgg) update(samples []int16) (values []float64, rawPeak float64) {
	n := len(samples)
	if n == 0 {
		return nil, 0
	}
	per := n / w.bars
	if per < 1 {
		per = 1
	}

	raw := make([]float64, w.bars)
	for i := 0; i < w.bars; i++ {
		start := i * per
		if start >= n {
			break
		}
		end := start + per
		if end > n {
			end = n
		}
		var sum float64
		for _, s := range samples[start:end] {
			v := float64(s) / 32768.0
			sum += v * v
		}
		raw[i] = math.Sqrt(sum / float64(end-start))
	}

	for _, v := range raw {
		if v > rawPeak {
			rawPeak = v
		}
	}

	w.peakHistory = append(w.peakHistory, rawPeak)
	if len(w.peakHistory) > peakWindowSize {
		w.peakHistory = w.peakHistory[len(w.peakHistory)-peakWindowSize:]
	}
	target := percentile(w.peakHistory, 0.85)
	if target < 0.005 {
		target = 0.005 // floor to prevent division blowup
	}
	w.peakLevel = w.peakLevel*0.8 + target*0.2

	values = make([]float64, w.bars)
	for i, v := range raw {
		val := v / w.peakLevel * 0.85
		if val > 1 {
			val = 1
		}
		values[i] = val
	}
	return values, rawPeak
}

// percentile returns the p-th (0..1) percentile with linear interpolation.
func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if len(s) == 1 {
		return s[0]
	}
	pos := p * float64(len(s)-1)
	lo := int(pos)
	hi := lo + 1
	if hi >= len(s) {
		return s[len(s)-1]
	}
	frac := pos - float64(lo)
	return s[lo]*(1-frac) + s[hi]*frac
}
