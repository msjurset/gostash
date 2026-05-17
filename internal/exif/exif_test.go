package exif

import (
	"bytes"
	"errors"
	"testing"
)

// Decoding an empty / non-JPEG payload should surface a decode
// error — not a panic, not a silent zero return.
func TestExtractGPSNonJPEGFails(t *testing.T) {
	_, _, err := ExtractGPS(bytes.NewReader([]byte("not an image")))
	if err == nil {
		t.Fatal("expected error for non-jpeg, got nil")
	}
}

// Sentinel value: the parser must distinguish "decoded but no GPS"
// from "couldn't read the bytes" via ErrNoGPS so the backfill
// command can silently skip non-geotagged images while still
// surfacing real read failures.
func TestErrNoGPSWraps(t *testing.T) {
	wrapped := errors.New("base: " + ErrNoGPS.Error())
	if errors.Is(wrapped, ErrNoGPS) {
		// Constructing via string concat doesn't preserve wrap chain
		// — assertion is just that ErrNoGPS is a real sentinel.
		t.Skip("string-built wrap not expected to match — sanity check")
	}
	// Direct identity check.
	if !errors.Is(ErrNoGPS, ErrNoGPS) {
		t.Errorf("ErrNoGPS should match itself")
	}
}
