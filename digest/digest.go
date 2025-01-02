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
	"time"
)

func ComputeHA1(username, realm, password string) string {
	return md5Hash(fmt.Sprintf("%s:%s:%s", username, realm, password))
}

type PasswordProvider interface {
	GetHA1(username string) (string, bool, error)
}

type DigestAuth struct {
	Realm            string
	PasswordProvider PasswordProvider
	Opaque           string
}

func NewDigestAuth(realm string, provider PasswordProvider) *DigestAuth {
	return &DigestAuth{
		Realm:            realm,
		PasswordProvider: provider,
		Opaque:           generateSecureRandom(),
	}
}

func (da *DigestAuth) Wrap(handler http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Check Authorization header
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Digest ") {
			log.Printf("\tno digest auth header")
			da.challenge(w)
			return
		}

		// Parse the Authorization header
		authParams := parseDigestHeader(authHeader)
		ha1, exists, err := da.PasswordProvider.GetHA1(authParams["username"])
		if err != nil {
			log.Printf("trying to get HA1 for %q: %v", authParams["username"], err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		if !exists {
			log.Printf("\tno such user")
			da.challenge(w)
			return
		}

		// Validate the response hash
		expectedResponse := da.calculateResponse(authParams, ha1, r.Method)
		if subtle.ConstantTimeCompare([]byte(authParams["response"]), []byte(expectedResponse)) != 1 {
			log.Printf("\twrong response")
			da.challenge(w)
			return
		}

		// If valid, call the wrapped handler
		ctx := context.WithValue(r.Context(), authenticatedUsername, authParams["username"])
		handler.ServeHTTP(w, r.WithContext(ctx))
	}
}

func AuthenticatedUsername(ctx context.Context) (string, bool) {
	val := ctx.Value(authenticatedUsername)
	if val == nil {
		return "", false
	}
	s, ok := val.(string)
	if !ok {
		log.Printf("authenticated username in context (%#v) is not a string??", val)
		return "", false
	}
	return s, true
}

type contextKey int

var (
	authenticatedUsername contextKey = 0
)

func (da *DigestAuth) challenge(w http.ResponseWriter) {
	nonce := generateNonce()
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

func generateNonce() string {
	timestamp := time.Now().Unix()
	randomPart := generateSecureRandom()
	return md5Hash(fmt.Sprintf("%d:%s", timestamp, randomPart))
}
