package digest

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// UserStore provides user credential lookup for digest authentication.
type UserStore interface {
	// GetHA1AndAuthContext returns the HA1 hash for the given username.
	// If found, returns the hash and a context with the authenticated user set.
	// If not found, returns found=false.
	GetHA1AndAuthContext(ctx context.Context, username string) (ha1 string, found bool, authCtx context.Context, err error)
}

const (
	// nonceMaxAge is how long a nonce remains valid.
	nonceMaxAge = 5 * time.Minute
	// nonceCleanupInterval is how often we clean up expired nonces.
	nonceCleanupInterval = 1 * time.Minute
	// nonceMaxCount limits memory usage by capping the number of stored nonces.
	nonceMaxCount = 10000
)

// ComputeHA1 computes the HA1 hash for HTTP Digest Authentication (RFC 2617).
// MD5 is mandated by the protocol and required for macOS WebDAV client compatibility.
// Security relies on using HTTPS for transport encryption.
func ComputeHA1(username, realm, password string) string {
	return md5Hash(fmt.Sprintf("%s:%s:%s", username, realm, password))
}

// nonceEntry tracks when a nonce was issued.
type nonceEntry struct {
	issuedAt time.Time
}

type DigestAuth struct {
	Realm     string
	UserStore UserStore
	Opaque    string

	noncesMu sync.Mutex
	nonces   map[string]nonceEntry
	closeCh  chan struct{}
}

func NewDigestAuth(realm string, userStore UserStore) *DigestAuth {
	da := &DigestAuth{
		Realm:     realm,
		UserStore: userStore,
		Opaque:    generateSecureRandom(),
		nonces:    make(map[string]nonceEntry),
		closeCh:   make(chan struct{}),
	}
	go da.cleanupNonces()
	return da
}

// Close stops the cleanup goroutine.
func (da *DigestAuth) Close() {
	close(da.closeCh)
}

// cleanupNonces periodically removes expired nonces.
func (da *DigestAuth) cleanupNonces() {
	ticker := time.NewTicker(nonceCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-da.closeCh:
			return
		case <-ticker.C:
			da.noncesMu.Lock()
			now := time.Now()
			for nonce, entry := range da.nonces {
				if now.Sub(entry.issuedAt) > nonceMaxAge {
					delete(da.nonces, nonce)
				}
			}
			da.noncesMu.Unlock()
		}
	}
}

// issueNonce creates a new nonce and records it.
func (da *DigestAuth) issueNonce() string {
	nonce := generateSecureRandom()
	da.noncesMu.Lock()
	defer da.noncesMu.Unlock()

	// If we have too many nonces, remove oldest ones
	if len(da.nonces) >= nonceMaxCount {
		// Find and remove oldest 10%
		type nonceAge struct {
			nonce    string
			issuedAt time.Time
		}
		var oldest []nonceAge
		for n, e := range da.nonces {
			oldest = append(oldest, nonceAge{n, e.issuedAt})
		}
		// Simple approach: just delete entries older than median age
		now := time.Now()
		for n, e := range da.nonces {
			if now.Sub(e.issuedAt) > nonceMaxAge/2 {
				delete(da.nonces, n)
			}
		}
		// If still too many, delete half randomly
		if len(da.nonces) >= nonceMaxCount {
			count := 0
			for n := range da.nonces {
				if count%2 == 0 {
					delete(da.nonces, n)
				}
				count++
			}
		}
	}

	da.nonces[nonce] = nonceEntry{issuedAt: time.Now()}
	return nonce
}

// validateNonce checks if a nonce was issued by us and is still valid.
func (da *DigestAuth) validateNonce(nonce string) bool {
	da.noncesMu.Lock()
	defer da.noncesMu.Unlock()

	entry, exists := da.nonces[nonce]
	if !exists {
		return false
	}
	if time.Since(entry.issuedAt) > nonceMaxAge {
		delete(da.nonces, nonce)
		return false
	}
	return true
}

func (da *DigestAuth) Wrap(handler http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Check Authorization header
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Digest ") {
			log.Printf("\t(no digest auth header)")
			da.challenge(w)
			return
		}

		// Parse the Authorization header
		authParams := parseDigestHeader(authHeader)

		// Validate the nonce was issued by us and is still valid
		if !da.validateNonce(authParams["nonce"]) {
			log.Printf("\t(invalid or expired nonce)")
			da.challenge(w)
			return
		}

		ctx := r.Context()
		ha1, found, authCtx, err := da.UserStore.GetHA1AndAuthContext(ctx, authParams["username"])
		if err != nil {
			log.Printf("trying to get HA1 for %q: %v", authParams["username"], err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		if !found {
			log.Printf("\t(no such user)")
			da.challenge(w)
			return
		}

		// Validate the response hash
		expectedResponse := da.calculateResponse(authParams, ha1, r.Method)
		if subtle.ConstantTimeCompare([]byte(authParams["response"]), []byte(expectedResponse)) != 1 {
			log.Printf("\t(wrong response)")
			da.challenge(w)
			return
		}

		// If valid, call the wrapped handler with the authenticated context
		handler.ServeHTTP(w, r.WithContext(authCtx))
	}
}

func (da *DigestAuth) challenge(w http.ResponseWriter) {
	nonce := da.issueNonce()
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(
		"Digest realm=\"%s\", nonce=\"%s\", opaque=\"%s\", qop=auth",
		da.Realm, nonce, da.Opaque,
	))
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
}

func (da *DigestAuth) calculateResponse(authParams map[string]string, ha1, method string) string {
	// HA2 = MD5(method:digestURI)
	ha2 := md5Hash(fmt.Sprintf("%s:%s", method, authParams["uri"]))

	// response = MD5(HA1:nonce:nonceCount:cnonce:qop:HA2)
	return md5Hash(fmt.Sprintf("%s:%s:%s:%s:%s:%s", ha1, authParams["nonce"], authParams["nc"], authParams["cnonce"], authParams["qop"], ha2))
}

func md5Hash(data string) string {
	hash := md5.Sum([]byte(data))
	return hex.EncodeToString(hash[:])
}

func parseDigestHeader(header string) map[string]string {
	params := make(map[string]string)
	header = strings.TrimPrefix(header, "Digest ")
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 {
			params[strings.ToLower(kv[0])] = strings.Trim(kv[1], "\"")
		}
	}
	return params
}

func generateSecureRandom() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		panic("Failed to generate secure random string")
	}
	return hex.EncodeToString(bytes)
}
