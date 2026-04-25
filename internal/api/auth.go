package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/argon2"
)

const (
	argonTime         uint32 = 3
	argonMemory       uint32 = 64 * 1024
	argonThreads      uint8  = 4
	argonKeyLen       uint32 = 32
	saltLen                  = 16
	maxLoginBodyBytes        = 4096
	jwtIssuer                = "sable-operator"
	jwtAudience              = "sable-web"
	jwtSubject               = "operator"
	jwtLeeway                = 5 * time.Second
	operatorTokenTTL         = time.Hour
)

// PasswordHash holds an Argon2id hash and its salt.
type PasswordHash struct {
	Hash []byte
	Salt []byte
}

type operatorClaims struct {
	jwt.RegisteredClaims
}

// HashPassword hashes a password with Argon2id using secure random salt.
func HashPassword(password string) *PasswordHash {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		panic("argon2 salt generation failed: " + err.Error())
	}
	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return &PasswordHash{Hash: hash, Salt: salt}
}

// verifyPassword checks a plaintext password against a stored Argon2id hash in constant time.
func verifyPassword(password string, stored *PasswordHash) bool {
	hash := argon2.IDKey([]byte(password), stored.Salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return subtle.ConstantTimeCompare(hash, stored.Hash) == 1
}

func newOperatorClaims(now time.Time) *operatorClaims {
	return &operatorClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   jwtSubject,
			Issuer:    jwtIssuer,
			Audience:  jwt.ClaimStrings{jwtAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(operatorTokenTTL)),
		},
	}
}

// loginHandler issues a signed JWT on valid operator credentials.
func loginHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Password string `json:"password"`
		}
		if !decodeJSONBody(w, r, &req, maxLoginBodyBytes) {
			return
		}
		if req.Password == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !verifyPassword(req.Password, cfg.OperatorPasswordHash) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, newOperatorClaims(time.Now()))
		signed, err := token.SignedString(cfg.JWTSecret)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"token": signed}) //nolint:errcheck
	}
}
