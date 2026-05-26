package gemini

import "testing"

// Locks in parser behavior so any future change to the marker
// shape stays compatible with the prompt the Mac and Android
// clients also use. If you add a new marker shape, add a row
// here AND update the corresponding Swift / Kotlin clients.
func TestParse(t *testing.T) {
	cases := []struct {
		name           string
		raw            string
		wantTitle      string
		wantNotes      string
		wantTranscript string
	}{
		{
			name: "standard three-line",
			raw: "TITLE: Eastern Bluebird (Sialia sialis)\n" +
				"NOTES: A small North American thrush with brilliant blue plumage.\n" +
				"TRANSCRIPT: NONE",
			wantTitle:      "Eastern Bluebird (Sialia sialis)",
			wantNotes:      "A small North American thrush with brilliant blue plumage.",
			wantTranscript: "",
		},
		{
			name: "transcript multi-line until eof",
			raw: "TITLE: Sign at Trailhead\n" +
				"NOTES: A wooden trail sign.\n" +
				"TRANSCRIPT: Welcome to Bear Mountain\nElevation 1284 ft\nNo dogs past this point",
			wantTitle:      "Sign at Trailhead",
			wantNotes:      "A wooden trail sign.",
			wantTranscript: "Welcome to Bear Mountain\nElevation 1284 ft\nNo dogs past this point",
		},
		{
			name: "markdown-wrapped markers",
			raw: "**TITLE:** Boletus edulis\n" +
				"**NOTES:** The classic porcini.\n" +
				"**TRANSCRIPT:** NONE",
			wantTitle:      "Boletus edulis",
			wantNotes:      "The classic porcini.",
			wantTranscript: "",
		},
		{
			name: "synonym markers (Common Name / Description)",
			raw: "Common Name: Red Maple\n" +
				"Description: A common deciduous tree native to eastern North America.",
			wantTitle:      "Red Maple",
			wantNotes:      "A common deciduous tree native to eastern North America.",
			wantTranscript: "",
		},
		{
			name:           "no markers — whole body becomes notes",
			raw:            "I think this might be a kind of fern but I'm not confident.",
			wantTitle:      "",
			wantNotes:      "I think this might be a kind of fern but I'm not confident.",
			wantTranscript: "",
		},
		{
			name: "transcript NONE collapses to empty",
			raw: "TITLE: Mushroom\n" +
				"NOTES: A small brown mushroom.\n" +
				"TRANSCRIPT: NONE",
			wantTitle:      "Mushroom",
			wantNotes:      "A small brown mushroom.",
			wantTranscript: "",
		},
		{
			// Known Swift parser behavior we're matching: extractValue
			// trims trailing `*_` characters from the value, which loses
			// the closing marker of an italic scientific name. Leading
			// marker survives because trimming strips equally from both
			// ends and the title starts with a non-marker character. If
			// we ever want to fix this, fix it in stash-mac first and
			// land the same change here.
			name: "trailing italic marker is lost (Swift parity)",
			raw: "TITLE: Boletus *edulis*\n" +
				"NOTES: details",
			wantTitle: "Boletus *edulis",
			wantNotes: "details",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Parse(tc.raw)
			if got.Title != tc.wantTitle {
				t.Errorf("Title:\n  got:  %q\n  want: %q", got.Title, tc.wantTitle)
			}
			if got.Notes != tc.wantNotes {
				t.Errorf("Notes:\n  got:  %q\n  want: %q", got.Notes, tc.wantNotes)
			}
			if got.Transcript != tc.wantTranscript {
				t.Errorf("Transcript:\n  got:  %q\n  want: %q", got.Transcript, tc.wantTranscript)
			}
		})
	}
}

func TestIsTransient(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"empty response", ErrEmptyResponse, true},
		{"missing key", ErrMissingKey, false},
		{"503 unavailable", &HTTPError{Status: 503, Body: "model overloaded"}, true},
		{"429 plain rate limit", &HTTPError{Status: 429, Body: "rate limit exceeded"}, true},
		{"429 free-tier quota", &HTTPError{Status: 429, Body: "quota exceeded: free_tier_requests"}, false},
		{"429 billing quota", &HTTPError{Status: 429, Body: "quota exceeded; please add billing"}, false},
		{"401 unauthorized", &HTTPError{Status: 401, Body: "invalid key"}, false},
		{"403 forbidden", &HTTPError{Status: 403, Body: "permission denied"}, false},
		{"500 server error", &HTTPError{Status: 500, Body: "internal"}, true},
		{"400 bad request", &HTTPError{Status: 400, Body: "bad request"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTransient(tc.err); got != tc.want {
				t.Errorf("IsTransient(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsKeyRejected(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"401", &HTTPError{Status: 401, Body: "..."}, true},
		{"403", &HTTPError{Status: 403, Body: "..."}, true},
		{"400 API_KEY_INVALID", &HTTPError{Status: 400, Body: `{"error":{"status":"INVALID_ARGUMENT","message":"API_KEY_INVALID"}}`}, true},
		{"400 unrelated", &HTTPError{Status: 400, Body: "bad json"}, false},
		{"429 quota", &HTTPError{Status: 429, Body: "quota"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsKeyRejected(tc.err); got != tc.want {
				t.Errorf("IsKeyRejected(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
