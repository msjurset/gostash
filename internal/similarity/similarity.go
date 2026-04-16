package similarity

import "strings"

// Trigrams returns the set of character trigrams in s (lowercased).
func Trigrams(s string) map[string]bool {
	s = strings.ToLower(s)
	set := make(map[string]bool)
	runes := []rune(s)
	for i := 0; i+3 <= len(runes); i++ {
		set[string(runes[i:i+3])] = true
	}
	return set
}

// Score returns the Jaccard similarity between two strings based on their
// trigram sets. Returns a value between 0.0 (no overlap) and 1.0 (identical).
func Score(a, b string) float64 {
	if a == b {
		return 1.0
	}
	ta := Trigrams(a)
	tb := Trigrams(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}

	intersection := 0
	for k := range ta {
		if tb[k] {
			intersection++
		}
	}

	union := len(ta) + len(tb) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}
