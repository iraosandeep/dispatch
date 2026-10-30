package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// requireAPIKey enforces "Authorization: Bearer <key>". Several keys may be
// valid at once to allow rotation without downtime.
func requireAPIKey(keys []string, next http.Handler) http.Handler {
	hashes := make([][32]byte, len(keys))
	for i, k := range keys {
		hashes[i] = sha256.Sum256([]byte(k))
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok || !matchKey(hashes, token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="dispatch"`)
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "missing or invalid API key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

// matchKey compares fixed-length hashes in constant time and checks every
// key, so timing reveals neither key contents nor which key matched.
func matchKey(hashes [][32]byte, token string) bool {
	h := sha256.Sum256([]byte(token))
	match := 0
	for i := range hashes {
		match |= subtle.ConstantTimeCompare(h[:], hashes[i][:])
	}
	return match == 1
}
