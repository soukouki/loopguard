package detect

import "testing"

func TestDetectLoop_BasicPeriod(t *testing.T) {
	// "abc" repeated 4 times = period 3, reps 4.
	// Redundant bytes: 3 * (4-1) = 9 > 5 threshold.
	w := []byte("abcabcabcabc")
	if !DetectLoop(w, 3, 3, 5) {
		t.Fatal("expected loop detected")
	}
}

func TestDetectLoop_ShortPeriodRequiresMoreRepeats(t *testing.T) {
	// 1-byte period "x" repeated 6 times = period 1, reps 6.
	// Redundant bytes: 1 * (6-1) = 5 > 4 threshold. Should detect with threshold 4.
	w := []byte("xxxxxx")
	if !DetectLoop(w, 1, 1, 4) {
		t.Fatal("expected loop detected for short period with enough repeats")
	}
}

func TestDetectLoop_ShortPeriodRejectedWithLowRepeats(t *testing.T) {
	// 1-byte period, 3 repeats. Redundant bytes: 1 * 2 = 2 <= 4 threshold. Rejected.
	w := []byte("xxx")
	if DetectLoop(w, 1, 1, 4) {
		t.Fatal("short period with low repeats should not detect")
	}
}

func TestDetectLoop_WhitespaceRejected(t *testing.T) {
	// Whitespace-only period should be rejected.
	w := make([]byte, 0)
	for i := 0; i < 6; i++ {
		w = append(w, []byte(" \n")...)
	}
	// Period = 2 (" \n"), reps = 3. Redundant: 2 * 2 = 4 > 1 threshold. But rejected.
	if DetectLoop(w, 2, 4, 1) {
		t.Fatal("whitespace-only period should not be detected as loop")
	}
}

func TestDetectLoop_TooShort(t *testing.T) {
	// "abc" x1 = period 3, reps 1. Redundant bytes = 0. Never triggers.
	w := []byte("abc")
	if DetectLoop(w, 3, 3, 0) {
		t.Fatal("buffer too short for any redundancy should not detect")
	}
}

func TestDetectLoop_PeriodOutOfBounds(t *testing.T) {
	// period = 2, but min allowed is 3.
	w := []byte("ababababab")
	if DetectLoop(w, 3, 3, 1) {
		t.Fatal("period below min should not detect")
	}
}

func TestDetectLoop_LongPeriodDetectedEarly(t *testing.T) {
	// 100-byte period, 3 repetitions. Redundant bytes: 100 * 2 = 200 > 150 threshold.
	base := make([]byte, 100)
	for i := range base {
		base[i] = byte('A' + (i % 26))
	}
	w := append(append(append([]byte{}, base...), base...), base...)
	if !DetectLoop(w, 50, 200, 150) {
		t.Fatal("expected loop detected for long period with just 3 repetitions")
	}
}
