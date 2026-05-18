// Package exif reads JPEG/TIFF EXIF metadata. Consumers include the
// image-capture pipeline (auto-fills model.Item.Location from GPS
// tags + model.Item.CapturedAt from DateTimeOriginal) and the
// backfill commands that retroactively process existing items.
package exif

import (
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/rwcarlsen/goexif/exif"
)

// ErrNoGPS is returned when the file decodes as EXIF cleanly but
// lacks parseable GPSLatitude / GPSLongitude tags. Distinct from
// the bare decode error so callers can treat "no location" as
// silent skip vs surface a real read failure.
var ErrNoGPS = errors.New("exif: no GPS tags")

// ErrNoCaptureTime is returned when EXIF decoded cleanly but had no
// usable DateTimeOriginal / CreateDate / DateTime tag. Callers
// should treat it as silent skip — falling back to a filesystem
// time signal is the standard recovery, not a real read failure.
var ErrNoCaptureTime = errors.New("exif: no capture time")

// Orientation reads the EXIF Orientation tag, defaulting to 1
// (no transform) when absent or unparseable. Callers should pass
// the same byte buffer they decode the image from.
//
// Values follow the EXIF spec:
//   1 = normal (no transform)
//   2 = mirror horizontal
//   3 = rotate 180
//   4 = mirror vertical
//   5 = mirror horizontal + rotate 270 CW (transpose)
//   6 = rotate 90 CW
//   7 = mirror horizontal + rotate 90 CW (transverse)
//   8 = rotate 270 CW (= 90 CCW)
func Orientation(r io.Reader) int {
	x, err := exif.Decode(r)
	if err != nil {
		return 1
	}
	tag, err := x.Get(exif.Orientation)
	if err != nil {
		return 1
	}
	v, err := tag.Int(0)
	if err != nil || v < 1 || v > 8 {
		return 1
	}
	return v
}

// ExtractGPS reads `r` as JPEG/TIFF EXIF and returns lat/lon when
// present. HEIC and other container formats are not supported by
// the underlying library — they return a decode error which the
// caller should treat the same as "no GPS available."
//
// Returns ErrNoGPS for files that decode but don't carry GPS data,
// so callers can `errors.Is(err, exif.ErrNoGPS)` to silently skip
// non-geotagged images without logging real failures.
func ExtractGPS(r io.Reader) (lat, lon float64, err error) {
	x, decodeErr := exif.Decode(r)
	if decodeErr != nil {
		return 0, 0, fmt.Errorf("decode exif: %w", decodeErr)
	}
	lat, lon, gpsErr := x.LatLong()
	if gpsErr != nil {
		return 0, 0, fmt.Errorf("%w: %v", ErrNoGPS, gpsErr)
	}
	// goexif returns NaN when GPS tags exist but are partial /
	// unparseable (saw this on several Pixel HDR+ shots). Treat the
	// same as "no GPS".
	if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) {
		return 0, 0, ErrNoGPS
	}
	// Some cameras embed a (0, 0) GPS sentinel when the lock fails.
	// Skip it — Null Island is rarely a real photo location.
	if lat == 0 && lon == 0 {
		return 0, 0, ErrNoGPS
	}
	// Defence in depth: reject anything outside the geographic range
	// so a future library quirk can't write garbage rows.
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return 0, 0, ErrNoGPS
	}
	return lat, lon, nil
}

// ExtractCaptureTime reads `r` as JPEG/TIFF EXIF and returns the
// best capture timestamp it can find. Preference order:
//   1. DateTimeOriginal — when the shutter fired
//   2. CreateDate (alias DateTimeDigitized) — usually equal to #1
//   3. DateTime (file modification timestamp written by the camera)
//
// Returns ErrNoCaptureTime when none of the three are parseable —
// the caller should fall back to the file's filesystem birth/mtime
// (or accept that the item has no capture time).
//
// EXIF stores times in the camera's local timezone with no offset.
// Without an offset tag, we can't recover the absolute moment — so
// we parse as time.Local and assume the user's machine clock is
// close enough. The Mac UI / clustering only need this for
// human-scale comparisons (same-day, same-trip), not nanosecond
// alignment.
func ExtractCaptureTime(r io.Reader) (time.Time, error) {
	x, decodeErr := exif.Decode(r)
	if decodeErr != nil {
		return time.Time{}, fmt.Errorf("decode exif: %w", decodeErr)
	}
	if t, err := x.DateTime(); err == nil && !t.IsZero() {
		return t, nil
	}
	// goexif's DateTime() walks DateTimeOriginal → CreateDate →
	// DateTime; only return ErrNoCaptureTime when *all* three fail.
	return time.Time{}, ErrNoCaptureTime
}
