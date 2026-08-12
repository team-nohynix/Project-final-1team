package query

import "testing"

func TestPercentile99(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if got := percentile99(nil); got != 0 {
			t.Fatalf("percentile99(nil) = %v, want 0", got)
		}
	})

	t.Run("single sample", func(t *testing.T) {
		if got := percentile99([]float64{42}); got != 42 {
			t.Fatalf("percentile99([42]) = %v, want 42", got)
		}
	})

	t.Run("unsorted input, does not mutate caller slice", func(t *testing.T) {
		samples := []float64{300, 100, 200}
		// idx = int(0.99*3) - 1 = 1 -> sorted [100,200,300][1] = 200
		if got := percentile99(samples); got != 200 {
			t.Fatalf("percentile99 = %v, want 200", got)
		}
		if samples[0] != 300 || samples[1] != 100 || samples[2] != 200 {
			t.Fatalf("input slice was mutated: %v", samples)
		}
	})

	t.Run("100 samples picks the 99th (index 98)", func(t *testing.T) {
		samples := make([]float64, 100)
		for i := range samples {
			samples[i] = float64(i + 1) // 1..100
		}
		if got := percentile99(samples); got != 99 {
			t.Fatalf("percentile99 = %v, want 99", got)
		}
	})
}
