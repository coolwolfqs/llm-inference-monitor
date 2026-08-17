package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"inference-hub-v3/src/shared"

	"github.com/gin-gonic/gin"
)

const (
	sessionCookieName = "ihub_session"
	sessionDuration   = 24 * time.Hour
)

var (
	loginAttempts   = make(map[string][]time.Time)
	loginAttemptsMu sync.Mutex
)

// sessionToken creates an HMAC-signed token for the given admin key.
func sessionToken(adminKey string) string {
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	mac := hmac.New(sha256.New, []byte(adminKey))
	mac.Write([]byte(ts))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(ts + ":" + sig))
}

// validateSession checks an ihub_session cookie value against the admin key.
func validateSession(token string, adminKey string) bool {
	if token == "" || adminKey == "" {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return false
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return false
	}
	ts, sig := parts[0], parts[1]

	// Verify HMAC
	mac := hmac.New(sha256.New, []byte(adminKey))
	mac.Write([]byte(ts))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return false
	}

	// Check expiry
	tsMs, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	age := time.Since(time.UnixMilli(tsMs))
	return age >= 0 && age < sessionDuration
}

// checkLoginRateLimit returns true if the IP is rate-limited (5 attempts/min).
func checkLoginRateLimit(ip string) bool {
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()
	cutoff := time.Now().Add(-1 * time.Minute)
	attempts := loginAttempts[ip]
	var filtered []time.Time
	for _, t := range attempts {
		if t.After(cutoff) {
			filtered = append(filtered, t)
		}
	}
	loginAttempts[ip] = filtered
	return len(filtered) >= 5
}

// recordLoginAttempt records a login attempt for rate limiting.
func recordLoginAttempt(ip string) {
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()
	loginAttempts[ip] = append(loginAttempts[ip], time.Now())
}

// setupSessionRoutes registers /api/auth/* endpoints.
func setupSessionRoutes(r *gin.Engine, adminKey string) {
	auth := r.Group("/api/auth")
	{
		auth.POST("/login", func(c *gin.Context) {
			ip := c.ClientIP()
			if checkLoginRateLimit(ip) {
				c.JSON(http.StatusTooManyRequests, gin.H{"error": "登录尝试过于频繁，请稍后再试"})
				return
			}

			// Accept JSON {"key":"..."} or form data key=...
			key := ""
			ct := c.GetHeader("Content-Type")
			if strings.HasPrefix(ct, "application/json") {
				var jsonBody struct {
					Key string `json:"key"`
				}
				if err := c.ShouldBindJSON(&jsonBody); err == nil {
					key = jsonBody.Key
				}
			} else {
				key = c.PostForm("key")
			}

			if key == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "请输入密钥"})
				return
			}

			recordLoginAttempt(ip)

			if key != adminKey {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "密钥错误"})
				return
			}

			token := sessionToken(adminKey)
			c.SetSameSite(http.SameSiteLaxMode)
			c.SetCookie(sessionCookieName, token,
				int(sessionDuration.Seconds()), "/", "", false, true)

			shared.Infof("[Auth] login success from %s", ip)
			c.JSON(http.StatusOK, gin.H{"ok": true, "user": "admin"})
		})

		auth.POST("/logout", func(c *gin.Context) {
			c.SetSameSite(http.SameSiteLaxMode)
			c.SetCookie(sessionCookieName, "", -1, "/", "", false, true)
			c.JSON(http.StatusOK, gin.H{"ok": true})
		})

		auth.GET("/check", func(c *gin.Context) {
			// Check cookie
			if token, err := c.Cookie(sessionCookieName); err == nil && token != "" {
				if validateSession(token, adminKey) {
					c.JSON(http.StatusOK, gin.H{"authenticated": true, "user": "admin"})
					return
				}
			}
			// Check header
			if c.GetHeader("X-Admin-Key") == adminKey {
				c.JSON(http.StatusOK, gin.H{"authenticated": true, "user": "admin"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"authenticated": false})
		})
	}
}

// sessionAuthMiddleware checks cookie OR header auth. Returns true if authenticated.
func sessionAuthMiddleware(c *gin.Context, adminKey string) bool {
	// Check X-Admin-Key header first (backward compat)
	if c.GetHeader("X-Admin-Key") == adminKey {
		return true
	}
	// Check session cookie
	if token, err := c.Cookie(sessionCookieName); err == nil && token != "" {
		if validateSession(token, adminKey) {
			return true
		}
	}
	return false
}
