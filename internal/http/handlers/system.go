// api/internal/http/handlers/system.go
package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"golive/internal/chat"
	"golive/pkg/logger"
	"golive/pkg/response"
)

type SystemHandler struct {
	DB       *pgxpool.Pool
	RDB      *redis.Client
	Logger   *logger.Logger
	ChatHubs map[string]*chat.Hub
}

func NewSystemHandler(db *pgxpool.Pool, rdb *redis.Client, log *logger.Logger) *SystemHandler {
	return &SystemHandler{
		DB:     db,
		RDB:    rdb,
		Logger: log,
	}
}

// @Summary Health Check
// @Description Check the health status of the API and its dependencies
// @Tags System
// @Produce json
// @Success 200 {object} map[string]interface{} "API is healthy"
// @Failure 503 {object} map[string]interface{} "Service unavailable - Database or Redis unhealthy"
// @Router /health [get]
func (h *SystemHandler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if err := h.DB.Ping(ctx); err != nil {
		response.Error(w, "Database unhealthy", http.StatusServiceUnavailable)
		return
	}

	if err := h.RDB.Ping(ctx).Err(); err != nil {
		response.Error(w, "Redis unhealthy", http.StatusServiceUnavailable)
		return
	}

	response.JSON(w, map[string]interface{}{
		"status":    "ok",
		"timestamp": time.Now(),
		"version":   "1.0.0",
	})
}

// @Summary System Metrics
// @Description Get system metrics and statistics
// @Tags System
// @Produce json
// @Success 200 {object} map[string]interface{} "System metrics including user count, active streams, etc."
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /metrics [get]
func (h *SystemHandler) HandleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var totalUsers, activeStreams int64
	h.DB.QueryRow(ctx, "SELECT COUNT(*) FROM users").Scan(&totalUsers)
	h.DB.QueryRow(ctx, "SELECT COUNT(*) FROM streams WHERE is_live = true").Scan(&activeStreams)

	response.JSON(w, map[string]interface{}{
		"total_users":     totalUsers,
		"active_streams":  activeStreams,
		"connected_chats": len(h.ChatHubs),
		"timestamp":       time.Now(),
	})
}

// @Summary List API Endpoints
// @Description Get a comprehensive list of all available API endpoints
// @Tags System
// @Produce json
// @Success 200 {object} map[string]interface{} "List of all API endpoints with methods and descriptions"
// @Router /endpoints [get]
func (h *SystemHandler) HandleListEndpoints(router chi.Routes) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var routes []map[string]string

		// Utiliser chi.Walk pour parcourir toutes les routes
		chi.Walk(router, func(method string, route string, handler http.Handler, middlewares ...func(http.Handler) http.Handler) error {
			routes = append(routes, map[string]string{
				"method":      method,
				"path":        route,
				"description": getEndpointDescription(method, route),
			})
			return nil
		})

		response.JSON(w, map[string]interface{}{
			"endpoints": routes,
			"total":     len(routes),
		})
	}
}

func getEndpointDescription(method, path string) string {
	descriptions := map[string]string{
		"GET /api/health":                                              "Health check",
		"GET /api/metrics":                                             "System metrics",
		"GET /api/endpoints":                                           "List all endpoints",
		"GET /api/streams":                                             "List public streams",
		"GET /api/streams/{key}":                                       "Get specific stream",
		"GET /api/streams/{key}/analytics":                             "Get stream analytics",
		"GET /api/categories":                                          "List stream categories",
		"POST /api/auth/register":                                      "User registration",
		"POST /api/auth/login":                                         "User login",
		"POST /api/auth/refresh":                                       "Refresh JWT token",
		"GET /api/auth/me":                                             "Get current user",
		"POST /api/auth/logout":                                        "User logout",
		"GET /api/users/me":                                            "Get current user",
		"GET /api/users/me/streams":                                    "Get user's streams",
		"GET /api/users/me/following":                                  "Get user's following list",
		"GET /api/users/me/followers":                                  "Get user's followers",
		"PUT /api/users/me":                                            "Update current user",
		"DELETE /api/users/me":                                         "Delete current user",
		"POST /api/users/{userID}/follow":                              "Follow user",
		"DELETE /api/users/{userID}/follow":                            "Unfollow user",
		"GET /api/me/streams":                                          "Get user's streams",
		"POST /api/me/streams":                                         "Create new stream",
		"PUT /api/me/streams/{key}":                                    "Update stream",
		"DELETE /api/me/streams/{key}":                                 "Delete stream",
		"POST /api/me/streams/{key}/start":                             "Start stream",
		"POST /api/me/streams/{key}/stop":                              "Stop stream",
		"GET /api/ws":                                                  "Realtime WebSocket connection",
		"GET /api/realtime/stats":                                      "Realtime server stats",
		"GET /api/realtime/channels":                                   "List realtime channels",
		"GET /api/realtime/channels/{channel}":                         "Get channel info",
		"GET /api/realtime/channels/{channel}/history":                 "Get channel history",
		"GET /api/realtime/channels/{channel}/presence":                "Get channel presence",
		"POST /api/realtime/channels/{channel}/publish":                "Publish to channel",
		"POST /api/realtime/channels/{channel}/broadcast":              "Broadcast system message",
		"DELETE /api/realtime/channels/{channel}/messages/{messageID}": "Delete message",
		"POST /api/realtime/channels/{channel}/clear":                  "Clear channel history",
		"POST /api/realtime/channels/{channel}/users/{userID}/timeout": "Timeout user",
		"POST /api/realtime/channels/{channel}/users/{userID}/ban":     "Ban user",
		"DELETE /api/realtime/channels/{channel}/users/{userID}/ban":   "Unban user",
		"PUT /api/realtime/channels/{channel}/settings":                "Update channel settings",
		"POST /api/realtime/channels/{channel}/access":                 "Grant channel access",
		"DELETE /api/realtime/channels/{channel}/access/{userID}":      "Revoke channel access",
		"POST /api/realtime/channels/{channel}/presence/sync":          "Sync channel presence",
		"POST /api/realtime/rpc":                                       "Execute realtime RPC",
	}

	key := method + " " + path
	if desc, exists := descriptions[key]; exists {
		return desc
	}
	return "No description available"
}
