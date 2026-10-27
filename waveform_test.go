package main

import "testing"

func TestWaveformAggBasics(t *testing.T) {
	agg := newWaveformAgg(waveformBars)

	values, rawPeak := agg.update(nil)
	if values != nil || rawPeak != 0 {
		t.Fatal("empty input should yield nothing")
	}

	values, rawPeak = agg.update(loudSamples(1600)) // 100ms at constant 2000
	if len(values) != waveformBars {
		t.Fatalf("len(values) = %d, want %d", len(values), waveformBars)
	}
	for i, v := range values {
		if v < 0 || v > 1 {
			t.Fatalf("values[%d] = %f out of [0,1]", i, v)
		}
	}
	// Constant 2000-amplitude signal: RMS = 2000/32768 ≈ 0.061.
	if rawPeak < 0.05 || rawPeak > 0.07 {
		t.Fatalf("rawPeak = %f, want ~0.061", rawPeak)
	}
	if rawPeak < silenceThreshold {
		t.Fatal("loud signal must be above the silence threshold")
	}
}

func TestWaveformAggSilenceBelowThreshold(t *testing.T) {
	agg := newWaveformAgg(waveformBars)
	quiet := make([]int16, 1600)
	for i := range quiet {
		quiet[i] = 50 // RMS ≈ 0.0015
	}
	_, rawPeak := agg.update(quiet)
	if rawPeak >= silenceThreshold {
		t.Fatalf("rawPeak = %f, should be below threshold", rawPeak)
	}
}

func TestWaveformAggNormalizesTowardFullScale(t *testing.T) {
	agg := newWaveformAgg(waveformBars)
	var values []float64
	// Feed a steady signal; normalization should adapt so bars approach 0.85.
	for i := 0; i < 60; i++ {
		values, _ = agg.update(loudSamples(1600))
	}
	var maxV float64
	for _, v := range values {
		if v > maxV {
			maxV = v
		}
	}
	if maxV < 0.5 {
		t.Fatalf("after adaptation max bar = %f, want >= 0.5", maxV)
	}
}

func TestWaveformAggShortChunk(t *testing.T) {
	agg := newWaveformAgg(waveformBars)
	// Fewer samples than bars must not panic and still return bar values.
	values, _ := agg.update(loudSamples(5))
	if len(values) != waveformBars {
		t.Fatalf("len(values) = %d", len(values))
	}
}

func TestPercentile(t *testing.T) {
	if p := percentile(nil, 0.85); p != 0 {
		t.Fatalf("empty percentile = %f", p)
	}
	if p := percentile([]float64{3}, 0.85); p != 3 {
		t.Fatalf("single percentile = %f", p)
	}
	xs := []float64{1, 2, 3, 4, 5}
	if p := percentile(xs, 0); p != 1 {
		t.Fatalf("p0 = %f", p)
	}
	if p := percentile(xs, 1); p != 5 {
		t.Fatalf("p100 = %f", p)
	}
	if p := percentile(xs, 0.5); p != 3 {
		t.Fatalf("p50 = %f", p)
	}
}
