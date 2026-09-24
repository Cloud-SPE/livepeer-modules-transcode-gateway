package server

import (
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/abr"
	"math"
	"testing"
)

func TestABRAuthorizationLimit(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		estimate, requested, ceiling, want int64
		bad                                bool
	}{
		{"headroom", 2182, 0, 1000000, 2728, false},
		{"explicit", 2182, 5000, 1000000, 5000, false},
		{"near ceiling", 900, 0, 1000, 1000, false},
		{"above ceiling", 1001, 0, 1000, 0, true},
		{"override above ceiling", 100, 1001, 1000, 0, true},
		{"override below estimate", 100, 99, 1000, 0, true},
		{"negative", 100, -1, 1000, 0, true},
		{"overflow", math.MaxInt64 - 1, 0, math.MaxInt64, math.MaxInt64, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := abrAuthorizationLimit(tc.estimate, tc.requested, tc.ceiling)
			if (err != nil) != tc.bad || got != tc.want {
				t.Fatalf("got %d, %v; want %d bad=%v", got, err, tc.want, tc.bad)
			}
		})
	}
}

func TestABREstimate(t *testing.T) {
	preset, _ := abr.Get("abr-standard")
	if got := estimateABRUnits(preset, 10); got != 2182 {
		t.Fatalf("got %d", got)
	}
	if estimateABRUnits(preset, 0) != estimateABRUnits(preset, 60) {
		t.Fatal("unknown duration default changed")
	}
	if got := estimateABRUnits(preset, math.MaxInt); got != math.MaxInt64 {
		t.Fatalf("overflow: %d", got)
	}
}
