package api

import "testing"

func TestConsistentPaceRange(t *testing.T) {
	for _, test := range []struct {
		name, fast, slow, average string
		unit                      float64
		valid                     bool
	}{
		{"same cleaned basis", "400", "600", "8", 1, true},
		{"partial weighted average", "300", "500", "7.222222222222222", 1, true},
		{"average faster than fastest", "500", "600", "7", 1, false},
		{"average slower than slowest", "400", "500", "9", 1, false},
		{"mile conversion", "700", "900", "8", 1.609344, true},
		{"invalid range", "600", "400", "8", 1, false},
		{"nonfinite data", "NaN", "600", "8", 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := consistentPaceRange(&test.fast, &test.slow, &test.average, test.unit); got != test.valid {
				t.Fatalf("valid=%t want %t", got, test.valid)
			}
		})
	}
}
