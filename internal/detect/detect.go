// Package detect implements loop detection based on the KMP prefix function
// (failure function) over accumulated byte windows, per specification section 4.
//
// The detection criterion is based on "redundant repetition volume": once the
// product of (period length) and (repeats - 1) exceeds a configurable byte
// threshold, a loop is considered established. This means short-period loops
// require many repetitions, while long-period loops are caught after just 2-3.
package detect

// prefixFunction computes the KMP prefix function (failure function) for
// the given byte slice. pi[i] is the length of the longest proper prefix
// of s[0..i] that is also a suffix of s[0..i].
//
// Complexity: O(n).
func prefixFunction(s []byte) []int {
	n := len(s)
	pi := make([]int, n)
	for i := 1; i < n; i++ {
		j := pi[i-1]
		for j > 0 && s[i] != s[j] {
			j = pi[j-1]
		}
		if s[i] == s[j] {
			j++
		}
		pi[i] = j
	}
	return pi
}

// isWhitespaceOnly returns true if the given byte range consists entirely
// of whitespace/blank characters. Used to reject whitespace-only periods
// and avoid false positives (specification section 4.2, hardcoded).
func isWhitespaceOnly(b []byte) bool {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			continue
		default:
			return false
		}
	}
	return true
}

// DetectLoop returns true if the accumulated window contains a periodic
// pattern (a loop) based on redundant repetition volume threshold.
//
// Parameters:
//   - window: the most recent bytes of the accumulated delta text.
//   - minPeriod, maxPeriod: accepted period length bounds (in bytes).
//   - thresholdBytes: redundant bytes of repetition required before a loop is
//     considered established. A loop is detected when:
//       period * (repeats - 1) > thresholdBytes
//
// Algorithm:
//   1. Compute the prefix function of the window.
//   2. Derive the minimal period: period = n - pi[n-1].
//   3. Validate the period is within bounds.
//   4. Compute repeats = n / period.
//   5. Check redundant volume: period * (repeats - 1) > thresholdBytes.
//   6. Reject whitespace-only periods.
//
// If the window is too short for any valid period, returns false.
func DetectLoop(window []byte, minPeriod, maxPeriod, thresholdBytes int) bool {
	n := len(window)
	if n == 0 {
		return false
	}
	pi := prefixFunction(window)
	period := n - pi[n-1]
	if period < minPeriod || period > maxPeriod {
		return false
	}
	if period == 0 {
		return false
	}
	reps := n / period
	redundantBytes := period * (reps - 1)
	if redundantBytes <= thresholdBytes {
		return false
	}
	if isWhitespaceOnly(window[:period]) {
		return false
	}
	return true
}
