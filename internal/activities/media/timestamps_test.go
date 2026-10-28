package media

import "testing"

func TestFrameTimestamps(t *testing.T) {
	got := frameTimestamps(10, 2, 30)
	assertFloats(t, []float64{0, 2, 4, 6, 8}, got)

	got = frameTimestamps(100, 2, 4)
	assertFloats(t, []float64{0, 25, 50, 75}, got)

	got = frameTimestamps(1.5, 2, 30)
	assertFloats(t, []float64{0}, got)

	if frameTimestamps(0, 2, 30) != nil {
		t.Fatal("unknown duration must be nil")
	}

	got = frameTimestamps(3600, 2, 1)
	assertFloats(t, []float64{0}, got)
}

func assertFloats(t *testing.T, want, got []float64) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("len %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("index %d: got %v want %v", i, got[i], want[i])
		}
	}
}
