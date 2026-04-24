package api

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// WithSecurityHeaders adds hardened HTTP response headers to every response.
func WithSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; connect-src 'self'; font-src 'self'; form-action 'self'; frame-ancestors 'none'; img-src 'self' data:; object-src 'none'; script-src 'self'; style-src 'self'")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
		w.Header().Set("X-Permitted-Cross-Domain-Policies", "none")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

var operatorTokenParser = jwt.NewParser(
	jwt.WithValidMethods([]string{"HS256"}),
	jwt.WithAudience(jwtAudience),
	jwt.WithIssuer(jwtIssuer),
	jwt.WithSubject(jwtSubject),
	jwt.WithLeeway(jwtLeeway),
)

// parseOperatorToken parses and validates an operator JWT.
func parseOperatorToken(tokenStr string, secret []byte) (*operatorClaims, error) {
	claims := &operatorClaims{}
	token, err := operatorTokenParser.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return secret, nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, jwt.ErrSignatureInvalid
	}
	return claims, nil
}

// requireJWT validates the Bearer token in the Authorization header.
func requireJWT(jwtSecret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			tokenStr := strings.TrimPrefix(auth, "Bearer ")
			if _, err := parseOperatorToken(tokenStr, jwtSecret); err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type bucket struct {
	count   int
	resetAt time.Time
}

type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{buckets: make(map[string]*bucket)}
}

func (rl *rateLimiter) allow(ip string, limit int, window time.Duration) (bool, time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	// Evict expired entries to prevent unbounded map growth under many unique IPs.
	for k, b := range rl.buckets {
		if now.After(b.resetAt) {
			delete(rl.buckets, k)
		}
	}
	b, ok := rl.buckets[ip]
	if !ok || now.After(b.resetAt) {
		rl.buckets[ip] = &bucket{count: 1, resetAt: now.Add(window)}
		return true, window
	}
	if b.count >= limit {
		return false, time.Until(b.resetAt)
	}
	b.count++
	return true, time.Until(b.resetAt)
}

// limitLogin wraps a handler with per-IP rate limiting (5 attempts per 60 seconds).
func limitLogin(rl *rateLimiter, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r.RemoteAddr)
		allowed, retryAfter := rl.allow(ip, 5, 60*time.Second)
		if !allowed {
			if retryAfter < time.Second {
				retryAfter = time.Second
			}
			w.Header().Set("Retry-After", strconv.Itoa(int((retryAfter+time.Second-1)/time.Second)))
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

func clientIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}
