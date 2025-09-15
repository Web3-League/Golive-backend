// api/internal/middleware/middleware.go
package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"mime"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/redis/go-redis/v9"

	"golive/internal/auth"
	"golive/internal/config"
	"golive/pkg/logger"
)

type contextKey string

const (
	UserContextKey contextKey = "user"
	UserIDKey      contextKey = "user_id"
)

// CORS middleware with configuration
func CORS(cfg config.CORSConfig) func(http.Handler) http.Handler {
	return cors.Handler(cors.Options{
		AllowedOrigins:   cfg.AllowedOrigins,
		AllowedMethods:   cfg.AllowedMethods,
		AllowedHeaders:   cfg.AllowedHeaders,
		ExposedHeaders:   cfg.ExposedHeaders,
		AllowCredentials: cfg.AllowCredentials,
		MaxAge:           int(cfg.MaxAge.Seconds()),
	})
}

// Structured logging middleware
func Logger(log *logger.Logger) func(http.Handler) http.Handler {
	return middleware.RequestLogger(&StructuredLogger{log})
}

type StructuredLogger struct {
	Logger *logger.Logger
}

func (l *StructuredLogger) NewLogEntry(r *http.Request) middleware.LogEntry {
	entry := &StructuredLoggerEntry{Logger: l.Logger}
	logFields := map[string]interface{}{
		"method":     r.Method,
		"url":        r.URL.Path,
		"proto":      r.Proto,
		"user_agent": r.Header.Get("User-Agent"),
		"remote_ip":  GetRealIP(r),
	}

	if reqID := middleware.GetReqID(r.Context()); reqID != "" {
		logFields["req_id"] = reqID
	}

	entry.Logger = l.Logger.WithFields(logFields)
	entry.Logger.Info("request started")
	return entry
}

type StructuredLoggerEntry struct {
	Logger *logger.Logger
}

func (l *StructuredLoggerEntry) Write(status, bytes int, header http.Header, elapsed time.Duration, extra interface{}) {
	l.Logger.With(
		"status", status,
		"bytes", bytes,
		"elapsed_ms", float64(elapsed.Nanoseconds())/1000000.0,
	).Info("request completed")
}

func (l *StructuredLoggerEntry) Panic(v interface{}, stack []byte) {
	l.Logger.With(
		"panic", fmt.Sprintf("%+v", v),
		"stack", string(stack),
	).Error("request panicked")
}

// Rate limiting middleware using Redis
func RateLimit(rdb *redis.Client, cfg config.RateLimitConfig) func(http.Handler) http.Handler {
	if !cfg.Enabled {
		return func(next http.Handler) http.Handler {
			return next
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			key := fmt.Sprintf("rate_limit:%s", GetRealIP(r))

			// Use Redis sliding window rate limiting
			now := time.Now().Unix()
			window := int64(60) // 1 minute window

			pipe := rdb.Pipeline()
			pipe.ZRemRangeByScore(ctx, key, "0", fmt.Sprintf("%d", now-window))
			pipe.ZCard(ctx, key)
			pipe.ZAdd(ctx, key, redis.Z{Score: float64(now), Member: now})
			pipe.Expire(ctx, key, time.Duration(window)*time.Second)
			
			results, err := pipe.Exec(ctx)
			if err != nil {
				// Log error but don't block request
				next.ServeHTTP(w, r)
				return
			}

			count := results[1].(*redis.IntCmd).Val()
			if count >= int64(cfg.RequestsPerMin) {
				w.Header().Set("X-RateLimit-Limit", strconv.Itoa(cfg.RequestsPerMin))
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(now+window, 10))
				http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
				return
			}

			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(cfg.RequestsPerMin))
			w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(int64(cfg.RequestsPerMin)-count-1, 10))
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(now+window, 10))

			next.ServeHTTP(w, r)
		})
	}
}

// JWT Authentication middleware
func Auth(jwtSecret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractToken(r)
			if token == "" {
				http.Error(w, "Missing authorization token", http.StatusUnauthorized)
				return
			}

			claims, err := auth.Verify(token, jwtSecret)
			if err != nil {
				http.Error(w, "Invalid token", http.StatusUnauthorized)
				return
			}

			// Add user info to context
			ctx := context.WithValue(r.Context(), UserIDKey, claims.UserID)
			ctx = context.WithValue(ctx, UserContextKey, claims)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Optional auth - doesn't fail if no token provided
func OptionalAuth(jwtSecret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractToken(r)
			if token != "" {
				if claims, err := auth.Verify(token, jwtSecret); err == nil {
					ctx := context.WithValue(r.Context(), UserIDKey, claims.UserID)
					ctx = context.WithValue(ctx, UserContextKey, claims)
					r = r.WithContext(ctx)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Recovery middleware with structured logging
func Recovery(log *logger.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					log.With(
						"error", err,
						"method", r.Method,
						"url", r.URL.Path,
						"remote_ip", GetRealIP(r),
					).Error("panic recovered")

					http.Error(w, "Internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// Security headers middleware
func Security() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Basic security headers
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("X-XSS-Protection", "1; mode=block")
			w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
			
			// CSP for API endpoints
			if strings.HasPrefix(r.URL.Path, "/api") {
				w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
			}

			next.ServeHTTP(w, r)
		})
	}
}

// Real IP extraction (handles proxies and load balancers)
func GetRealIP(r *http.Request) string {
	// Check X-Forwarded-For header
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first IP in the chain
		ips := strings.Split(xff, ",")
		if len(ips) > 0 {
			return strings.TrimSpace(ips[0])
		}
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// Check CF-Connecting-IP (Cloudflare)
	if cfip := r.Header.Get("CF-Connecting-IP"); cfip != "" {
		return cfip
	}

	// Fallback to RemoteAddr
	return r.RemoteAddr
}

// Extract JWT token from Authorization header or cookie
func extractToken(r *http.Request) string {
	// Try Authorization header first
	auth := r.Header.Get("Authorization")
	if auth != "" {
		parts := strings.SplitN(auth, " ", 2)
		if len(parts) == 2 && strings.ToLower(parts[0]) == "bearer" {
			return parts[1]
		}
	}

	// Try cookie as fallback
	if cookie, err := r.Cookie("auth_token"); err == nil {
		return cookie.Value
	}

	return ""
}

func ContentType(contentType string) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
                ct := r.Header.Get("Content-Type")

                // Pas de body -> on laisse passer (utile pour POST sans body)
                if (r.ContentLength == 0 || r.Body == nil) && ct == "" {
                    next.ServeHTTP(w, r)
                    return
                }

                mt, _, err := mime.ParseMediaType(ct) // gère "application/json; charset=utf-8"
                if err != nil || mt != contentType {
                    http.Error(w, fmt.Sprintf("Content-Type must be %s", contentType), http.StatusUnsupportedMediaType)
                    return
                }
            }
            next.ServeHTTP(w, r)
        })
    }
}

// Request size limiting
func LimitRequestSize(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxBytes {
				http.Error(w, "Request too large", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// Helper functions to extract user info from context
func GetUserID(ctx context.Context) string {
	if userID, ok := ctx.Value(UserIDKey).(string); ok {
		return userID
	}
	return ""
}

func GetUserClaims(ctx context.Context) (*auth.Claims, bool) {
	if claims, ok := ctx.Value(UserContextKey).(*auth.Claims); ok {
		return claims, true
	}
	return nil, false
}