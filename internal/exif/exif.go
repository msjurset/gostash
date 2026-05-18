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

// Camera is the structured subset of EXIF that surfaces in the Mac
// detail view's "Capture device" row (Make / Model / aperture /
// shutter / focal length / ISO / pixel dimensions). All fields are
// optional — cameras vary wildly in what they emit. JSON tags align
// with the items.metadata.camera shape so the Mac can decode
// directly from item.metadata without an intermediate parse.
type Camera struct {
	Make          string  `json:"make,omitempty"`
	Model         string  `json:"model,omitempty"`
	Lens          string  `json:"lens,omitempty"`
	FNumber       float64 `json:"f_number,omitempty"`
	Exposure      string  `json:"exposure,omitempty"`
	FocalLengthMM float64 `json:"focal_length_mm,omitempty"`
	ISO           int     `json:"iso,omitempty"`
	Width         int     `json:"width,omitempty"`
	Height        int     `json:"height,omitempty"`
}

// HasAny returns true when at least one Camera field is populated.
// Callers use this to decide whether to attach the struct to
// items.metadata at all — no point writing an all-empty camera
// object.
func (c Camera) HasAny() bool {
	return c.Make != "" || c.Model != "" || c.Lens != "" ||
		c.FNumber != 0 || c.Exposure != "" ||
		c.FocalLengthMM != 0 || c.ISO != 0 ||
		c.Width != 0 || c.Height != 0
}

// ExtractCamera reads `r` as JPEG/TIFF EXIF and returns whatever
// camera-shape fields it can find. Missing tags leave the
// corresponding Camera field zero-valued — the caller checks
// `Camera.HasAny()` to decide if the result is worth keeping.
//
// Decode errors (non-JPEG, no EXIF block, etc.) are returned as-is
// so callers can distinguish read failures from "decoded fine but
// no camera info."
func ExtractCamera(r io.Reader) (Camera, error) {
	x, err := exif.Decode(r)
	if err != nil {
		return Camera{}, fmt.Errorf("decode exif: %w", err)
	}
	var cam Camera
	cam.Make = stringTag(x, exif.Make)
	cam.Model = stringTag(x, exif.Model)
	cam.Lens = stringTag(x, exif.LensModel)
	cam.FNumber = ratTagFloat(x, exif.FNumber)
	if exp, ok := exposureString(x); ok {
		cam.Exposure = exp
	}
	cam.FocalLengthMM = ratTagFloat(x, exif.FocalLength)
	cam.ISO = intTag(x, exif.ISOSpeedRatings)
	cam.Width = intTag(x, exif.PixelXDimension)
	cam.Height = intTag(x, exif.PixelYDimension)
	return cam, nil
}

// stringTag returns the trimmed string value of an EXIF tag, or ""
// when the tag is absent or unparseable. Quotes that EXIF
// libraries sometimes wrap around string values are stripped.
func stringTag(x *exif.Exif, name exif.FieldName) string {
	t, err := x.Get(name)
	if err != nil {
		return ""
	}
	s, err := t.StringVal()
	if err != nil {
		return ""
	}
	// Strip stray surrounding NULs / whitespace some cameras emit
	// in fixed-width string slots.
	return trimNul(s)
}

func trimNul(s string) string {
	out := make([]byte, 0, len(s))
	for _, b := range []byte(s) {
		if b == 0 {
			continue
		}
		out = append(out, b)
	}
	// Then trim normal whitespace.
	start, end := 0, len(out)
	for start < end && (out[start] == ' ' || out[start] == '\t') {
		start++
	}
	for end > start && (out[end-1] == ' ' || out[end-1] == '\t') {
		end--
	}
	return string(out[start:end])
}

// ratTagFloat converts a rational EXIF tag (num/denom) to a float.
// Returns 0 when the tag is missing, denom is zero, or any parse
// fails — the caller treats 0 as "no value."
func ratTagFloat(x *exif.Exif, name exif.FieldName) float64 {
	t, err := x.Get(name)
	if err != nil {
		return 0
	}
	num, den, err := t.Rat2(0)
	if err != nil || den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}

// intTag returns the first int value of an EXIF tag (lots of tags
// are stored as 1-element int arrays). 0 on absence / parse fail.
func intTag(x *exif.Exif, name exif.FieldName) int {
	t, err := x.Get(name)
	if err != nil {
		return 0
	}
	v, err := t.Int(0)
	if err != nil {
		return 0
	}
	return v
}

// exposureString formats ExposureTime as "1/N" when num is 1 and
// den is positive (the typical camera case), as "Ns" when num >
// den (long exposure), or as the bare decimal otherwise. Returns
// (string, false) when the tag is absent or unparseable so the
// caller can leave Camera.Exposure as the zero value.
func exposureString(x *exif.Exif) (string, bool) {
	t, err := x.Get(exif.ExposureTime)
	if err != nil {
		return "", false
	}
	num, den, err := t.Rat2(0)
	if err != nil || den == 0 {
		return "", false
	}
	if num == 1 {
		return fmt.Sprintf("1/%d", den), true
	}
	if num > den {
		return fmt.Sprintf("%.1fs", float64(num)/float64(den)), true
	}
	// Fractional but not 1/N — normalize to 1/N approx for the
	// common "1/47 = 25/1176" weirdness some cameras emit.
	approx := float64(den) / float64(num)
	return fmt.Sprintf("1/%d", int(approx+0.5)), true
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
