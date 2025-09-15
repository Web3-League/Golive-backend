// api/cmd/server/main.go
// @title GoLive API
// @version 1.0
// @description Live streaming platform API with chat
// @host localhost:8080
// @BasePath /api
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"golive/internal/config"
	"golive/internal/db"
	httpapi "golive/internal/http"
	"golive/internal/models"
	"golive/pkg/logger"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		fmt.Printf("Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Initialize logger
	log, err := logger.New(cfg.Logging)
	if err != nil {
		fmt.Printf("Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Sync()

	log.Info("Starting GoLive API server", 
		"version", "1.0.0",
		"port", cfg.Server.Port,
		"environment", os.Getenv("ENV"))

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize database
	log.Info("Connecting to database...")
	pool, err := db.New(ctx, cfg.Database.URL)
	if err != nil {
		log.Fatal("Failed to connect to database", "error", err)
	}
	defer pool.Close()

	// Run database migrations
	log.Info("Running database migrations...")
	if err := models.Migrate(ctx, pool); err != nil {
		log.Fatal("Failed to run migrations", "error", err)
	}

	// Initialize Redis
	log.Info("Connecting to Redis...")
	rdb := redis.NewClient(&redis.Options{
		Addr:         cfg.Redis.Addr,
		Password:     cfg.Redis.Password,
		DB:           cfg.Redis.DB,
		PoolSize:     cfg.Redis.PoolSize,
		MinIdleConns: cfg.Redis.MinIdleConns,
	})
	defer rdb.Close()

	// Test Redis connection
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatal("Failed to connect to Redis", "error", err)
	}

	// Initialize HTTP server
	server := httpapi.NewServer(pool, rdb, cfg, log)
	
	httpServer := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:      server.Routes(),
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	// Start server in goroutine
	serverErrors := make(chan error, 1)
	go func() {
		log.Info("API server listening", 
			"address", httpServer.Addr,
			"read_timeout", cfg.Server.ReadTimeout,
			"write_timeout", cfg.Server.WriteTimeout)
		
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErrors <- err
		}
	}()

	// Wait for interrupt signal or server error
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErrors:
		log.Fatal("Server error", "error", err)
		
	case sig := <-interrupt:
		log.Info("Shutdown signal received", "signal", sig.String())
		
		// Give outstanding requests time to complete
		shutdownCtx, shutdownCancel := context.WithTimeout(ctx, cfg.Server.ShutdownTimeout)
		defer shutdownCancel()

		log.Info("Shutting down server...")
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Error("Graceful shutdown failed", "error", err)
			
			// Force close if graceful shutdown fails
			if closeErr := httpServer.Close(); closeErr != nil {
				log.Error("Force close failed", "error", closeErr)
			}
		}

		// Close database connections
		log.Info("Closing database connections...")
		pool.Close()

		// Close Redis connections
		log.Info("Closing Redis connections...")
		if err := rdb.Close(); err != nil {
			log.Error("Failed to close Redis connection", "error", err)
		}

		log.Info("Server stopped gracefully")
	}
}

// Health check function that can be used by Docker
func healthCheck(cfg *config.Config) error {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	resp, err := client.Get(fmt.Sprintf("http://%s:%d/health", cfg.Server.Host, cfg.Server.Port))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check failed with status: %d", resp.StatusCode)
	}

	return nil
}