package gemini

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Sentinel errors returned by Identify. The daemon's worker
// branches on these to decide between "back off and retry next
// tick" and "give up on this item until something changes" — same
// classification logic as the Swift `isTransientIdentifyError`.
var (
	ErrMissingKey    = errors.New("gemini: missing API key")
	ErrEmptyResponse = errors.New("gemini: empty response")
)

// HTTPError wraps a non-2xx response so callers can branch on
// status code. Body is truncated upstream so a verbose
// quota-exceeded JSON doesn't bloat logs.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("gemini http %d: %s", e.Status, e.Body)
}

// IsTransient reports whether the error is one the daemon should
// retry later on its own (network blip, model overload, plain
// rate-limit) vs. one it should stop attempting until human
// intervention (bad key, quota exhausted, key rotated).
//
// **Critical**: HTTP 429 from Gemini covers BOTH per-minute rate
// limits AND daily / free-tier quota exhaustion. We need to read
// the body — "quota", "free_tier", "billing" markers — to tell
// them apart. Treating quota as transient burns the rest of the
// user's budget retrying calls that won't clear for hours.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrEmptyResponse) {
		// Empty response after a 200 — model didn't return anything
		// we could parse (often due to safety filters or malformed
		// inputs). This costs tokens every time! Do NOT treat as
		// transient, otherwise the daemon loops infinitely.
		return false
	}
	if errors.Is(err, ErrMissingKey) {
		return false
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		body := strings.ToLower(httpErr.Body)
		switch httpErr.Status {
		case 503:
			return true
		case 429:
			// Quota markers — permanent within the current
			// window; do NOT retry.
			if strings.Contains(body, "quota") ||
				strings.Contains(body, "free_tier") ||
				strings.Contains(body, "billing") {
				return false
			}
			// Plain rate limit — retry.
			return true
		case 401, 403:
			// Authentication / authorization — won't clear
			// without a key refresh. Daemon stops attempting
			// and logs once.
			return false
		default:
			if httpErr.Status >= 500 {
				return true
			}
			return false
		}
	}
	// Network-shaped errors — DNS, timeout, connection refused —
	// all worth a retry.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	return false
}

// IsKeyRejected reports whether the error is specifically "Gemini
// rejected the key" — 401 / 403, or a 400 carrying API_KEY_INVALID.
// Used by the worker to stop polling and surface a clear "run
// `stash auth refresh-gemini`" message rather than thrashing on
// stale credentials.
func IsKeyRejected(err error) bool {
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	if httpErr.Status == 401 || httpErr.Status == 403 {
		return true
	}
	if httpErr.Status == 400 {
		body := strings.ToLower(httpErr.Body)
		if strings.Contains(body, "api_key_invalid") ||
			strings.Contains(body, "api key not valid") {
			return true
		}
	}
	return false
}
