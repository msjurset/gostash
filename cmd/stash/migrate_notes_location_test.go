package main

import (
	"strings"
	"testing"
)

// The pin-URL pattern the mobile app embedded historically. Tests
// the regex + parseNotesLocation against every shape we've seen
// in the user's actual stash (pin first, prose after; prose first,
// pin in the middle; missing trailing newline; mixed coords).
func TestParseNotesLocation(t *testing.T) {
	tests := []struct {
		name    string
		notes   string
		wantLat float64
		wantLon float64
		wantOK  bool
	}{
		{
			name:    "canonical mobile pattern",
			notes:   "📍 https://maps.google.com/?q=33.7544,-84.6272\n\nThis is a Tickseed.",
			wantLat: 33.7544, wantLon: -84.6272, wantOK: true,
		},
		{
			name:    "no space after pin",
			notes:   "📍https://maps.google.com/?q=12.345,-67.890",
			wantLat: 12.345, wantLon: -67.890, wantOK: true,
		},
		{
			name:    "apple maps host accepted",
			notes:   "📍 https://maps.apple.com/?q=1.5,-2.5",
			wantLat: 1.5, wantLon: -2.5, wantOK: true,
		},
		{
			name:    "extra params before q",
			notes:   "📍 https://maps.google.com/?z=18&q=33.7,-84.6",
			wantLat: 33.7, wantLon: -84.6, wantOK: true,
		},
		{
			name:   "no pin emoji = no match",
			notes:  "Check https://maps.google.com/?q=33.7,-84.6",
			wantOK: false,
		},
		{
			name:   "out-of-range lat rejected",
			notes:  "📍 https://maps.google.com/?q=100.5,-84.6",
			wantOK: false,
		},
		{
			name:   "missing comma",
			notes:  "📍 https://maps.google.com/?q=33.7",
			wantOK: false,
		},
		{
			name:   "no q param",
			notes:  "📍 https://maps.google.com/place/foo",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lat, lon, _, ok := parseNotesLocation(tt.notes)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if lat != tt.wantLat || lon != tt.wantLon {
				t.Errorf("got %v,%v want %v,%v", lat, lon, tt.wantLat, tt.wantLon)
			}
		})
	}
}

func TestStripLineFromNotes(t *testing.T) {
	tests := []struct {
		name  string
		notes string
		line  string
		want  string
	}{
		{
			name:  "pin line at top with trailing blank",
			notes: "📍 https://maps.google.com/?q=1,2\n\nProse follows.",
			line:  "📍 https://maps.google.com/?q=1,2",
			want:  "Prose follows.",
		},
		{
			name:  "pin line in the middle",
			notes: "Before.\n📍 https://maps.google.com/?q=1,2\nAfter.",
			line:  "📍 https://maps.google.com/?q=1,2",
			want:  "Before.\nAfter.",
		},
		{
			name:  "pin line at the end",
			notes: "Some prose.\n📍 https://maps.google.com/?q=1,2",
			line:  "📍 https://maps.google.com/?q=1,2",
			want:  "Some prose.",
		},
		{
			name:  "line not present is a no-op",
			notes: "Plain notes.",
			line:  "absent line",
			want:  "Plain notes.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripLineFromNotes(tt.notes, tt.line)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// Belt-and-braces: parse-then-strip must produce notes without
// the pin URL, regardless of where it appeared.
func TestParseAndStripRoundTrip(t *testing.T) {
	notes := "📍 https://maps.google.com/?q=33.7544,-84.6272\n\nIdentified text body."
	lat, lon, line, ok := parseNotesLocation(notes)
	if !ok {
		t.Fatal("parse failed")
	}
	if lat == 0 || lon == 0 {
		t.Fatal("zero coords")
	}
	stripped := stripLineFromNotes(notes, line)
	if strings.Contains(stripped, "📍") {
		t.Errorf("strip left pin in notes: %q", stripped)
	}
	if !strings.Contains(stripped, "Identified text body.") {
		t.Errorf("strip removed too much: %q", stripped)
	}
}
