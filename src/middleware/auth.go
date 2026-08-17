package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"hash"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

func sha256Hash() hash.Hash                    { return sha256.New() }
func decodeBase64Raw(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
func encodeBase64Raw(b []byte) string          { return base64.RawURLEncoding.EncodeToString(b) }

// sessionTokenValid validates an ihub_session cookie HMAC token.
func sessionTokenValid(token string, adminKey string) bool {
	if token == "" || adminKey == "" {
		return false
	}
	decoded, err := decodeBase64Raw(token)
	if err != nil {
		return false
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return false
	}
	ts, sig := parts[0], parts[1]
	mac := hmac.New(sha256Hash, []byte(adminKey))
	mac.Write([]byte(ts))
	expected := encodeBase64Raw(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return false
	}
	// Parse timestamp and check 24h expiry
	tsMs, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	age := time.Since(time.UnixMilli(tsMs))
	return age >= 0 && age < 24*time.Hour
}

// AuthMiddleware validates API key for protected endpoints. Accepts either
// X-Admin-Key header or ihub_session cookie (BFF session auth).
func AuthMiddleware() gin.HandlerFunc {
	adminKey := os.Getenv("ADMIN_KEY")

	return func(c *gin.Context) {
		// 1. Check X-Admin-Key header (backward compat for API clients)
		key := c.GetHeader("X-Admin-Key")
		if key == "" {
			authHeader := c.GetHeader("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				key = strings.TrimPrefix(authHeader, "Bearer ")
			}
		}

		if adminKey == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error":   "auth_unconfigured",
				"message": "ADMIN_KEY is not configured; write operation refused",
			})
			c.Abort()
			return
		}

		// Accept valid header key
		if key != "" && hmac.Equal([]byte(key), []byte(adminKey)) {
			c.Next()
			return
		}

		// 2. Check ihub_session cookie (BFF session auth)
		if token, err := c.Cookie("ihub_session"); err == nil && token != "" {
			if sessionTokenValid(token, adminKey) {
				c.Next()
				return
			}
		}

		c.JSON(http.StatusForbidden, gin.H{
			"error":   "unauthorized",
			"message": "Valid admin key or session required for this operation",
		})
		c.Abort()
	}
}

// RateLimiter implements a simple token bucket rate limiter.
type RateLimiter struct {
	rate     int // tokens per second
	burst    int // max burst
	tokens   float64
	lastTime time.Time
	mu       sync.Mutex
}

func NewRateLimiter(rate, burst int) *RateLimiter {
	return &RateLimiter{
		rate:     rate,
		burst:    burst,
		tokens:   float64(burst),
		lastTime: time.Now(),
	}
}

func (rl *RateLimiter) Allow() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(rl.lastTime).Seconds()
	rl.tokens += elapsed * float64(rl.rate)
	if rl.tokens > float64(rl.burst) {
		rl.tokens = float64(rl.burst)
	}
	rl.lastTime = now

	if rl.tokens >= 1.0 {
		rl.tokens -= 1.0
		return true
	}
	return false
}

func RateLimitMiddleware(rate, burst int) gin.HandlerFunc {
	limiter := NewRateLimiter(rate, burst)

	return func(c *gin.Context) {
		if !limiter.Allow() {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":   "rate_limited",
				"message": "Too many requests. Please slow down.",
			})
			c.Abort()
			return
		}
		c.Next()
	}
}
