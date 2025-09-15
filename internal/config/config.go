// api/internal/config/config.go
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Server   ServerConfig
	Database DatabaseConfig
	Redis    RedisConfig
	JWT      JWTConfig
	CORS     CORSConfig
	RateLimit RateLimitConfig
	Logging   LoggingConfig
	MediaMTX  MediaMTXConfig
}

type ServerConfig struct {
	Port            int
	Host            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
}

type DatabaseConfig struct {
	URL         string
	MaxConns    int
	MinConns    int
	MaxLifetime time.Duration
	MaxIdleTime time.Duration
}

type RedisConfig struct {
	Addr         string
	Password     string
	DB           int
	PoolSize     int
	MinIdleConns int
}

type JWTConfig struct {
	Secret     string
	Expiration time.Duration
	Issuer     string
}

type CORSConfig struct {
	AllowedOrigins   []string
	AllowedMethods   []string
	AllowedHeaders   []string
	ExposedHeaders   []string
	AllowCredentials bool
	MaxAge           time.Duration
}

type RateLimitConfig struct {
	Enabled        bool
	RequestsPerMin int
	BurstSize      int
	CleanupPeriod  time.Duration
}

type LoggingConfig struct {
	Level  string
	Format string // json, text
	Output string // stdout, stderr, file
	File   string
}

type MediaMTXConfig struct {
	APIEndpoint string
	HLSBaseURL  string
	RTMPPort    int
}

// Load loads configuration from environment variables with sensible defaults
func Load() (*Config, error) {
	cfg := &Config{
		Server: ServerConfig{
			Port:            getEnvInt("PORT", 8080),
			Host:            getEnv("HOST", "0.0.0.0"),
			ReadTimeout:     getEnvDuration("READ_TIMEOUT", 15*time.Second),
			WriteTimeout:    getEnvDuration("WRITE_TIMEOUT", 15*time.Second),
			IdleTimeout:     getEnvDuration("IDLE_TIMEOUT", 60*time.Second),
			ShutdownTimeout: getEnvDuration("SHUTDOWN_TIMEOUT", 30*time.Second),
		},
		Database: DatabaseConfig{
			URL:         getEnv("DATABASE_URL", "postgres://golive:golive@localhost:5432/golive?sslmode=disable"),
			MaxConns:    getEnvInt("DB_MAX_CONNS", 25),
			MinConns:    getEnvInt("DB_MIN_CONNS", 5),
			MaxLifetime: getEnvDuration("DB_MAX_LIFETIME", 5*time.Minute),
			MaxIdleTime: getEnvDuration("DB_MAX_IDLE_TIME", 1*time.Minute),
		},
		Redis: RedisConfig{
			Addr:         getEnv("REDIS_ADDR", "localhost:6379"),
			Password:     getEnv("REDIS_PASSWORD", ""),
			DB:           getEnvInt("REDIS_DB", 0),
			PoolSize:     getEnvInt("REDIS_POOL_SIZE", 10),
			MinIdleConns: getEnvInt("REDIS_MIN_IDLE", 5),
		},
		JWT: JWTConfig{
			Secret:     getEnv("JWT_SECRET", "dev_secret_change_me"),
			Expiration: getEnvDuration("JWT_EXPIRATION", 24*time.Hour),
			Issuer:     getEnv("JWT_ISSUER", "golive"),
		},
		CORS: CORSConfig{
			AllowedOrigins:   getEnvStringSlice("CORS_ALLOWED_ORIGINS", []string{"http://localhost:3000"}),
			AllowedMethods:   getEnvStringSlice("CORS_ALLOWED_METHODS", []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}),
			AllowedHeaders:   getEnvStringSlice("CORS_ALLOWED_HEADERS", []string{"*"}),
			ExposedHeaders:   getEnvStringSlice("CORS_EXPOSED_HEADERS", []string{"X-Total-Count"}),
			AllowCredentials: getEnvBool("CORS_ALLOW_CREDENTIALS", true),
			MaxAge:           getEnvDuration("CORS_MAX_AGE", 12*time.Hour),
		},
		RateLimit: RateLimitConfig{
			Enabled:        getEnvBool("RATE_LIMIT_ENABLED", true),
			RequestsPerMin: getEnvInt("RATE_LIMIT_RPM", 60),
			BurstSize:      getEnvInt("RATE_LIMIT_BURST", 10),
			CleanupPeriod:  getEnvDuration("RATE_LIMIT_CLEANUP", 1*time.Minute),
		},
		Logging: LoggingConfig{
			Level:  getEnv("LOG_LEVEL", "info"),
			Format: getEnv("LOG_FORMAT", "json"),
			Output: getEnv("LOG_OUTPUT", "stdout"),
			File:   getEnv("LOG_FILE", ""),
		},
		MediaMTX: MediaMTXConfig{
			APIEndpoint: getEnv("MEDIAMTX_API", "http://mediamtx:9997"),
			HLSBaseURL:  getEnv("HLS_BASE_URL", "http://localhost:8888"),
			RTMPPort:    getEnvInt("RTMP_PORT", 1935),
		},
	}

	// Validation
	if cfg.JWT.Secret == "dev_secret_change_me" && getEnv("ENV", "development") == "production" {
		return nil, fmt.Errorf("JWT_SECRET must be set in production")
	}

	return cfg, nil
}

// Helper functions for environment variable parsing
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			return parsed
		}
	}
	return defaultValue
}

func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil {
			return parsed
		}
	}
	return defaultValue
}

func getEnvStringSlice(key string, defaultValue []string) []string {
	if value := os.Getenv(key); value != "" {
		return strings.Split(value, ",")
	}
	return defaultValue
}