package middleware

import (
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// AuthMiddleware validates API key for protected endpoints.
func AuthMiddleware() gin.HandlerFunc {
	adminKey := os.Getenv("ADMIN_KEY")

	return func(c *gin.Context) {
		key := c.GetHeader("X-Admin-Key")
		if key == "" {
			authHeader := c.GetHeader("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				key = strings.TrimPrefix(authHeader, "Bearer ")
			}
		}

		if adminKey == "" {
			c.Next()
			return
		}

		if key == "" || key != adminKey {
			c.JSON(http.StatusForbidden, gin.H{
				"error":   "unauthorized",
				"message": "Valid admin key required for this operation",
			})
			c.Abort()
			return
		}
		c.Next()
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
