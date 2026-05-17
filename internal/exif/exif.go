// Package exif reads JPEG/TIFF EXIF metadata. The only consumer
// today is the image-capture pipeline that auto-fills
// model.Item.Location from GPS tags on ingest, plus the backfill
// command that retroactively processes existing items.
package exif

import (
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/rwcarlsen/goexif/exif"
)

// ErrNoGPS is returned when the file decodes as EXIF cleanly but
// lacks parseable GPSLatitude / GPSLongitude tags. Distinct from
// the bare decode error so callers can treat "no location" as
// silent skip vs surface a real read failure.
var ErrNoGPS = errors.New("exif: no GPS tags")

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
