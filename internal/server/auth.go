package server

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/msjurset/gostash/internal/config"
)

// requireBearer returns middleware that enforces the bearer token on
// every request. Constant-time compare to avoid leaking the token via
// timing. On mismatch: 401 with a `WWW-Authenticate: Bearer` hint and
// no body (preserves the standard).
func requireBearer(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := extractBearer(r.Header.Get("Authorization"))
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="stash"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requirePaidTier checks if the request is paid if config.Get().PaidTierEnabled is true.
// Mismatches are rejected with HTTP 402 Payment Required and a JSON body.
func (s *Server) requirePaidTier(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if config.Get().PaidTierEnabled {
			paid := false
			if s.IsPaidRequest != nil {
				paid = s.IsPaidRequest(r)
			} else {
				paid = r.Header.Get("X-Stash-Paid") == "true" || r.Header.Get("X-Stash-Paid-Tier") == "true"
			}
			if !paid {
				writeJSON(w, http.StatusPaymentRequired, map[string]string{
					"error":   "payment_required",
					"message": "This feature requires a premium subscription.",
				})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func extractBearer(authHeader string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(authHeader, prefix) {
		return ""
	}
	return strings.TrimSpace(authHeader[len(prefix):])
}
