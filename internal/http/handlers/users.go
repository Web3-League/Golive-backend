// api/internal/http/handlers/users.go
package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	mw "golive/internal/middleware"
	"golive/pkg/logger"
	"golive/pkg/response"
)

type UsersHandler struct {
	DB     *pgxpool.Pool
	Logger *logger.Logger
}

func NewUsersHandler(db *pgxpool.Pool, log *logger.Logger) *UsersHandler {
	return &UsersHandler{
		DB:     db,
		Logger: log,
	}
}

// @Summary Get Current User Profile
// @Description Get the profile information of the currently authenticated user
// @Tags Users
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]interface{} "User profile information"
// @Failure 401 {object} map[string]interface{} "Unauthorized"
// @Failure 404 {object} map[string]interface{} "User not found"
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /users/me [get]
func (h *UsersHandler) HandleGetCurrentUser(w http.ResponseWriter, r *http.Request) {
	userID := mw.GetUserID(r.Context())
	if userID == "" {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	ctx := r.Context()

	var user struct {
		ID          string    `json:"id" example:"123e4567-e89b-12d3-a456-426614174000"`
		Username    string    `json:"username" example:"johndoe"`
		DisplayName string    `json:"display_name" example:"John Doe"`
		Email       string    `json:"email" example:"user@example.com"`
		Avatar      *string   `json:"avatar_url" example:"https://example.com/avatar.jpg"`
		Bio         *string   `json:"bio" example:"Content creator and streamer"`
		CreatedAt   time.Time `json:"created_at" example:"2024-01-01T00:00:00Z"`
	}

	err := h.DB.QueryRow(ctx, `
		SELECT id, username, display_name, email, avatar_url, bio, created_at
		FROM users WHERE id = $1
	`, userID).Scan(
		&user.ID, &user.Username, &user.DisplayName,
		&user.Email, &user.Avatar, &user.Bio, &user.CreatedAt,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			response.Error(w, "User not found", http.StatusNotFound)
		} else {
			h.Logger.Error("Failed to get user", "error", err)
			response.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}

	response.JSON(w, user)
}

// @Summary Get Current User's Streams
// @Description Get all streams belonging to the currently authenticated user
// @Tags Users
// @Security BearerAuth
// @Produce json
// @Success 200 {array} map[string]interface{} "List of user's streams"
// @Failure 401 {object} map[string]interface{} "Unauthorized"
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /users/me/streams [get]
func (h *UsersHandler) HandleGetUserStreams(w http.ResponseWriter, r *http.Request) {
	userID := mw.GetUserID(r.Context())
	if userID == "" {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	ctx := r.Context()

	rows, err := h.DB.Query(ctx, `
		SELECT id, key, title, description, category, language, tags, 
		       is_public, is_mature, is_live, current_viewers, 
		       started_at, created_at, updated_at, ingest_key_hint
		FROM streams 
		WHERE user_id = $1 
		ORDER BY created_at DESC
	`, userID)

	if err != nil {
		h.Logger.Error("Failed to get user streams", "error", err)
		response.Error(w, "Failed to get streams", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var streams []map[string]interface{}

	for rows.Next() {
		var stream struct {
			ID             string     `json:"id"`
			Key            string     `json:"key"`
			Title          string     `json:"title"`
			Description    *string    `json:"description"`
			Category       *string    `json:"category"`
			Language       *string    `json:"language"`
			Tags           []string   `json:"tags"`
			IsPublic       bool       `json:"is_public"`
			IsMature       bool       `json:"is_mature"`
			IsLive         bool       `json:"is_live"`
			CurrentViewers int        `json:"current_viewers"`
			StartedAt      *time.Time `json:"started_at"`
			CreatedAt      time.Time  `json:"created_at"`
			UpdatedAt      time.Time  `json:"updated_at"`
			IngestHint     *string    `json:"ingest_hint"`
		}

		err := rows.Scan(
			&stream.ID, &stream.Key, &stream.Title, &stream.Description,
			&stream.Category, &stream.Language, &stream.Tags,
			&stream.IsPublic, &stream.IsMature, &stream.IsLive,
			&stream.CurrentViewers, &stream.StartedAt, &stream.CreatedAt, &stream.UpdatedAt, &stream.IngestHint,
		)

		if err != nil {
			h.Logger.Error("Failed to scan stream row", "error", err)
			continue
		}

		streamData := map[string]interface{}{
			"id":              stream.ID,
			"key":             stream.Key,
			"title":           stream.Title,
			"description":     stream.Description,
			"category":        stream.Category,
			"language":        stream.Language,
			"tags":            stream.Tags,
			"is_public":       stream.IsPublic,
			"is_mature":       stream.IsMature,
			"is_live":         stream.IsLive,
			"current_viewers": stream.CurrentViewers,
			"viewer_count":    stream.CurrentViewers, // Alias pour compatibilité frontend
			"started_at":      stream.StartedAt,
			"created_at":      stream.CreatedAt,
			"updated_at":      stream.UpdatedAt,
		}

		// Ajouter les URLs
		if stream.IsLive {
			streamData["hls_url"] = fmt.Sprintf("http://localhost:8888/%s/index.m3u8", stream.Key)
		}
		streamData["rtmp_url"] = fmt.Sprintf("rtmp://localhost:%d", 1935)
		streamData["stream_key"] = stream.Key
		if stream.IngestHint != nil {
			streamData["ingest_hint"] = *stream.IngestHint
		}

		streams = append(streams, streamData)
	}

	response.JSON(w, streams)
}

// @Summary Update Current User Profile
// @Description Update the profile information of the currently authenticated user (Not implemented yet)
// @Tags Users
// @Security BearerAuth
// @Accept json
// @Produce json
// @Success 200 {object} map[string]interface{} "User updated successfully"
// @Failure 501 {object} map[string]interface{} "Not implemented"
// @Router /users/me [put]
func (h *UsersHandler) HandleUpdateCurrentUser(w http.ResponseWriter, r *http.Request) {
	response.Error(w, "Not implemented", http.StatusNotImplemented)
}

// @Summary Delete Current User Account
// @Description Delete the currently authenticated user's account (Not implemented yet)
// @Tags Users
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]interface{} "User deleted successfully"
// @Failure 501 {object} map[string]interface{} "Not implemented"
// @Router /users/me [delete]
func (h *UsersHandler) HandleDeleteCurrentUser(w http.ResponseWriter, r *http.Request) {
	response.Error(w, "Not implemented", http.StatusNotImplemented)
}

// @Summary Follow User
// @Description Follow another user (Not implemented yet)
// @Tags Users
// @Security BearerAuth
// @Param userID path string true "User ID to follow"
// @Produce json
// @Success 200 {object} map[string]interface{} "User followed successfully"
// @Failure 501 {object} map[string]interface{} "Not implemented"
// @Router /users/{userID}/follow [post]
func (h *UsersHandler) HandleFollowUser(w http.ResponseWriter, r *http.Request) {
	response.Error(w, "Not implemented", http.StatusNotImplemented)
}

// @Summary Unfollow User
// @Description Unfollow a user (Not implemented yet)
// @Tags Users
// @Security BearerAuth
// @Param userID path string true "User ID to unfollow"
// @Produce json
// @Success 200 {object} map[string]interface{} "User unfollowed successfully"
// @Failure 501 {object} map[string]interface{} "Not implemented"
// @Router /users/{userID}/follow [delete]
func (h *UsersHandler) HandleUnfollowUser(w http.ResponseWriter, r *http.Request) {
	response.Error(w, "Not implemented", http.StatusNotImplemented)
}

// @Summary Get Following List
// @Description Get list of users that the current user is following (Not implemented yet)
// @Tags Users
// @Security BearerAuth
// @Produce json
// @Success 200 {array} map[string]interface{} "List of followed users"
// @Failure 501 {object} map[string]interface{} "Not implemented"
// @Router /users/me/following [get]
func (h *UsersHandler) HandleGetFollowing(w http.ResponseWriter, r *http.Request) {
	response.Error(w, "Not implemented", http.StatusNotImplemented)
}

// @Summary Get Followers List
// @Description Get list of users that are following the current user (Not implemented yet)
// @Tags Users
// @Security BearerAuth
// @Produce json
// @Success 200 {array} map[string]interface{} "List of followers"
// @Failure 501 {object} map[string]interface{} "Not implemented"
// @Router /users/me/followers [get]
func (h *UsersHandler) HandleGetFollowers(w http.ResponseWriter, r *http.Request) {
	response.Error(w, "Not implemented", http.StatusNotImplemented)
}
