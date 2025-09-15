// api/internal/http/handlers/streams.go
package handlers

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"

	"golive/internal/chat"
	"golive/internal/config"
	mw "golive/internal/middleware"
	"golive/pkg/logger"
	"golive/pkg/response"
)

type StreamsHandler struct {
	DB       *pgxpool.Pool
	Config   *config.Config
	Logger   *logger.Logger
	Validate *validator.Validate
	ChatHubs map[string]*chat.Hub
}

func generateStreamKey() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func hashIngestKey(key string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(key), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// === DTOs (centralisés ici pour éviter les doublons) ===

type CreateStreamRequest struct {
	Title       string   `json:"title" validate:"required,max=100" example:"My Awesome Stream"`
	Description string   `json:"description" validate:"max=500" example:"This is my streaming description"`
	Category    string   `json:"category" validate:"max=50" example:"Gaming"`
	Language    string   `json:"language" validate:"max=10" example:"en"`
	Tags        []string `json:"tags" validate:"max=10,dive,max=20" example:"gaming,live,fun"`
	IsPublic    *bool    `json:"is_public" example:"true"`
	IsMature    *bool    `json:"is_mature" example:"false"`
}

type UpdateStreamRequest struct {
	Title       *string  `json:"title,omitempty" validate:"omitempty,max=100" example:"Updated Stream Title"`
	Description *string  `json:"description,omitempty" validate:"omitempty,max=500" example:"Updated description"`
	Category    *string  `json:"category,omitempty" validate:"omitempty,max=50" example:"Art"`
	Language    *string  `json:"language,omitempty" validate:"omitempty,max=10" example:"fr"`
	Tags        []string `json:"tags,omitempty" validate:"omitempty,max=10,dive,max=20" example:"art,creative"`
	IsPublic    *bool    `json:"is_public,omitempty" example:"false"`
	IsMature    *bool    `json:"is_mature,omitempty" example:"true"`
}

func NewStreamsHandler(db *pgxpool.Pool, cfg *config.Config, log *logger.Logger, validate *validator.Validate, chatHubs map[string]*chat.Hub) *StreamsHandler {
	return &StreamsHandler{
		DB:       db,
		Config:   cfg,
		Logger:   log,
		Validate: validate,
		ChatHubs: chatHubs,
	}
}

// =============================================================================
// LIST PUBLIC STREAMS
// =============================================================================

// @Summary List Public Streams
// @Description Get list of public streams with pagination and filtering
// @Tags Streams
// @Produce json
// @Param limit query int false "Number of streams to return (max 100)" default(20)
// @Param offset query int false "Number of streams to skip" default(0)
// @Param category query string false "Filter by category"
// @Param live query boolean false "Filter only live streams"
// @Success 200 {object} map[string]interface{} "List of streams with pagination"
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /streams [get]
func (h *StreamsHandler) HandleListStreams(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}

	offset := 0
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	category := r.URL.Query().Get("category")
	onlyLive := r.URL.Query().Get("live") == "true"

	query := `
		SELECT s.id, s.key, s.title, s.description, s.category, s.language, s.tags,
		       s.is_live, s.current_viewers, s.thumbnail_url, s.created_at,
		       u.username, u.display_name, u.avatar_url
		FROM streams s
		JOIN users u ON s.user_id = u.id
		WHERE s.is_public = true
	`
	args := []interface{}{}
	argCount := 0

	if onlyLive {
		query += " AND s.is_live = true"
	}

	if category != "" {
		argCount++
		query += fmt.Sprintf(" AND s.category = $%d", argCount)
		args = append(args, category)
	}

	query += " ORDER BY s.current_viewers DESC, s.created_at DESC"

	argCount++
	query += fmt.Sprintf(" LIMIT $%d", argCount)
	args = append(args, limit)

	argCount++
	query += fmt.Sprintf(" OFFSET $%d", argCount)
	args = append(args, offset)

	rows, err := h.DB.Query(ctx, query, args...)
	if err != nil {
		h.Logger.Error("Failed to query streams", "error", err)
		response.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var streams []map[string]interface{}
	for rows.Next() {
		var stream struct {
			ID             string
			Key            string
			Title          string
			Description    *string
			Category       *string
			Language       string
			Tags           []string
			IsLive         bool
			CurrentViewers int
			ThumbnailURL   *string
			CreatedAt      time.Time
			Username       string
			DisplayName    string
			AvatarURL      *string
		}

		if err := rows.Scan(
			&stream.ID, &stream.Key, &stream.Title, &stream.Description,
			&stream.Category, &stream.Language, &stream.Tags, &stream.IsLive,
			&stream.CurrentViewers, &stream.ThumbnailURL, &stream.CreatedAt,
			&stream.Username, &stream.DisplayName, &stream.AvatarURL,
		); err != nil {
			h.Logger.Error("Failed to scan stream row", "error", err)
			continue
		}

		item := map[string]interface{}{
			"id":              stream.ID,
			"key":             stream.Key,
			"title":           stream.Title,
			"description":     stream.Description,
			"category":        stream.Category,
			"language":        stream.Language,
			"tags":            stream.Tags,
			"is_live":         stream.IsLive,
			"current_viewers": stream.CurrentViewers,
			"thumbnail_url":   stream.ThumbnailURL,
			"created_at":      stream.CreatedAt,
			"streamer": map[string]interface{}{
				"username":     stream.Username,
				"display_name": stream.DisplayName,
				"avatar_url":   stream.AvatarURL,
			},
		}
		if stream.IsLive {
			item["hls_url"] = fmt.Sprintf("%s/%s/index.m3u8", h.Config.MediaMTX.HLSBaseURL, stream.Key)
		}
		streams = append(streams, item)
	}

	var total int64
	countQuery := "SELECT COUNT(*) FROM streams s WHERE s.is_public = true"
	countArgs := []interface{}{}

	if onlyLive {
		countQuery += " AND s.is_live = true"
	}
	if category != "" {
		countQuery += " AND s.category = $1"
		countArgs = append(countArgs, category)
	}
	h.DB.QueryRow(ctx, countQuery, countArgs...).Scan(&total)

	response.JSON(w, map[string]interface{}{
		"streams": streams,
		"pagination": map[string]interface{}{
			"total":  total,
			"limit":  limit,
			"offset": offset,
		},
	})
}

// =============================================================================
// LIST CURRENT USER STREAMS
// =============================================================================

// @Summary Get Current User's Streams
// @Description Get list of streams for the authenticated user
// @Tags Streams
// @Security BearerAuth
// @Produce json
// @Success 200 {array} map[string]interface{} "User's streams"
// @Failure 401 {object} map[string]interface{} "Unauthorized"
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /me/streams [get]
func (h *StreamsHandler) HandleGetUserStreams(w http.ResponseWriter, r *http.Request) {
	userID := mw.GetUserID(r.Context())
	if userID == "" {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	ctx := r.Context()

	rows, err := h.DB.Query(ctx, `
		SELECT id, key, title, description, category, language, tags, 
		       is_public, is_mature, is_live, current_viewers, 
		       started_at, created_at, updated_at
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
		var srow struct {
			ID             string
			Key            string
			Title          string
			Description    *string
			Category       *string
			Language       *string
			Tags           []string
			IsPublic       bool
			IsMature       bool
			IsLive         bool
			CurrentViewers int
			StartedAt      *time.Time
			CreatedAt      time.Time
			UpdatedAt      time.Time
		}
		if err := rows.Scan(
			&srow.ID, &srow.Key, &srow.Title, &srow.Description,
			&srow.Category, &srow.Language, &srow.Tags, &srow.IsPublic, &srow.IsMature,
			&srow.IsLive, &srow.CurrentViewers, &srow.StartedAt, &srow.CreatedAt, &srow.UpdatedAt,
		); err != nil {
			h.Logger.Error("Failed to scan stream row", "error", err)
			continue
		}

		item := map[string]interface{}{
			"id":              srow.ID,
			"key":             srow.Key,
			"title":           srow.Title,
			"description":     srow.Description,
			"category":        srow.Category,
			"language":        srow.Language,
			"tags":            srow.Tags,
			"is_public":       srow.IsPublic,
			"is_mature":       srow.IsMature,
			"is_live":         srow.IsLive,
			"current_viewers": srow.CurrentViewers,
			"viewer_count":    srow.CurrentViewers,
			"started_at":      srow.StartedAt,
			"created_at":      srow.CreatedAt,
			"updated_at":      srow.UpdatedAt,
			"rtmp_url":        fmt.Sprintf("rtmp://localhost:%d", 1935),
			"stream_key":      srow.Key,
		}
		if srow.IsLive {
			item["hls_url"] = fmt.Sprintf("%s/%s/index.m3u8", h.Config.MediaMTX.HLSBaseURL, srow.Key)
		}
		streams = append(streams, item)
	}

	response.JSON(w, streams)
}

// =============================================================================
// GET ONE STREAM
// =============================================================================

// @Summary Get Stream by Key
// @Description Get specific stream information by stream key
// @Tags Streams
// @Produce json
// @Param key path string true "Stream Key"
// @Success 200 {object} map[string]interface{} "Stream information"
// @Failure 400 {object} map[string]interface{} "Stream key required"
// @Failure 404 {object} map[string]interface{} "Stream not found"
// @Router /streams/{key} [get]
func (h *StreamsHandler) HandleGetStream(w http.ResponseWriter, r *http.Request) {
	streamKey := chi.URLParam(r, "key")
	if streamKey == "" {
		response.Error(w, "Stream key required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	var stream struct {
		ID             string
		UserID         string
		Key            string
		Title          string
		Description    *string
		Category       *string
		Language       string
		Tags           []string
		IsLive         bool
		IsPublic       bool
		IsMature       bool
		CurrentViewers int
		PeakViewers    int
		TotalViews     int64
		FollowerCount  int64
		ThumbnailURL   *string
		StartedAt      *time.Time
		CreatedAt      time.Time
		Username       string
		DisplayName    string
		AvatarURL      *string
		Bio            *string
	}

	err := h.DB.QueryRow(ctx, `
		SELECT s.id, s.user_id, s.key, s.title, s.description, s.category, s.language,
		       s.tags, s.is_live, s.is_public, s.is_mature, s.current_viewers, s.peak_viewers,
		       s.total_views, s.follower_count, s.thumbnail_url, s.started_at, s.created_at,
		       u.username, u.display_name, u.avatar_url, u.bio
		FROM streams s
		JOIN users u ON s.user_id = u.id
		WHERE s.key = $1
	`, streamKey).Scan(
		&stream.ID, &stream.UserID, &stream.Key, &stream.Title, &stream.Description,
		&stream.Category, &stream.Language, &stream.Tags, &stream.IsLive, &stream.IsPublic,
		&stream.IsMature, &stream.CurrentViewers, &stream.PeakViewers, &stream.TotalViews,
		&stream.FollowerCount, &stream.ThumbnailURL, &stream.StartedAt, &stream.CreatedAt,
		&stream.Username, &stream.DisplayName, &stream.AvatarURL, &stream.Bio,
	)
	if err != nil {
		response.Error(w, "Stream not found", http.StatusNotFound)
		return
	}

	if !stream.IsPublic {
		userID := mw.GetUserID(r.Context())
		if userID != stream.UserID {
			response.Error(w, "Stream not found", http.StatusNotFound)
			return
		}
	}

	data := map[string]interface{}{
		"id":              stream.ID,
		"key":             stream.Key,
		"title":           stream.Title,
		"description":     stream.Description,
		"category":        stream.Category,
		"language":        stream.Language,
		"tags":            stream.Tags,
		"is_live":         stream.IsLive,
		"is_public":       stream.IsPublic,
		"is_mature":       stream.IsMature,
		"current_viewers": stream.CurrentViewers,
		"viewer_count":    stream.CurrentViewers,
		"peak_viewers":    stream.PeakViewers,
		"total_views":     stream.TotalViews,
		"follower_count":  stream.FollowerCount,
		"thumbnail_url":   stream.ThumbnailURL,
		"started_at":      stream.StartedAt,
		"created_at":      stream.CreatedAt,
		"streamer": map[string]interface{}{
			"id":           stream.UserID,
			"username":     stream.Username,
			"display_name": stream.DisplayName,
			"avatar_url":   stream.AvatarURL,
			"bio":          stream.Bio,
		},
	}
	if stream.IsLive {
		data["hls_url"] = fmt.Sprintf("%s/%s/index.m3u8", h.Config.MediaMTX.HLSBaseURL, stream.Key)
	}

	response.JSON(w, data)
}

// =============================================================================
// CREATE
// =============================================================================

// @Summary Create New Stream
// @Description Create a new stream for the authenticated user
// @Tags Streams
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param request body CreateStreamRequest true "Stream creation data"
// @Success 200 {object} map[string]interface{} "Stream created successfully"
// @Failure 400 {object} map[string]interface{} "Validation error"
// @Failure 401 {object} map[string]interface{} "Authentication required"
// @Failure 409 {object} map[string]interface{} "User already has an active stream"
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /me/streams [post]
func (h *StreamsHandler) HandleCreateStream(w http.ResponseWriter, r *http.Request) {
	var req CreateStreamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if err := h.Validate.Struct(&req); err != nil {
		response.ValidationError(w, err)
		return
	}

	userID := mw.GetUserID(r.Context())
	if userID == "" {
		response.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	ctx := r.Context()

	var hasActive bool
	if err := h.DB.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM streams WHERE user_id = $1 AND is_live = true)",
		userID).Scan(&hasActive); err != nil {
		h.Logger.Error("Database error", "error", err)
		response.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if hasActive {
		response.Error(w, "You already have an active stream", http.StatusConflict)
		return
	}

	streamKey, err := generateStreamKey()
	if err != nil {
		h.Logger.Error("Failed to generate stream key", "error", err)
		response.Error(w, "Failed to create stream", http.StatusInternalServerError)
		return
	}
	ingestKey, err := generateStreamKey()
	if err != nil {
		h.Logger.Error("Failed to generate ingest key", "error", err)
		response.Error(w, "Failed to create stream", http.StatusInternalServerError)
		return
	}
	ingestHash, err := hashIngestKey(ingestKey)
	if err != nil {
		h.Logger.Error("Failed to hash ingest key", "error", err)
		response.Error(w, "Failed to create stream", http.StatusInternalServerError)
		return
	}
	ingestHint := ""
	if len(ingestKey) >= 4 {
		ingestHint = ingestKey[len(ingestKey)-4:]
	}

	isPublic := true
	if req.IsPublic != nil {
		isPublic = *req.IsPublic
	}
	isMature := false
	if req.IsMature != nil {
		isMature = *req.IsMature
	}

	var streamID string
	err = h.DB.QueryRow(ctx, `
		INSERT INTO streams (user_id, key, title, description, category, language, tags, is_public, is_mature, ingest_key_hash, ingest_key_hint)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id
	`, userID, streamKey, req.Title, req.Description, req.Category, req.Language, req.Tags, isPublic, isMature, ingestHash, ingestHint).Scan(&streamID)
	if err != nil {
		h.Logger.Error("Failed to create stream", "error", err)
		response.Error(w, "Failed to create stream", http.StatusInternalServerError)
		return
	}

	h.Logger.Info("Stream created", "stream_id", streamID, "user_id", userID, "key", streamKey)

	response.JSON(w, map[string]interface{}{
		"id":          streamID,
		"key":         streamKey,
		"title":       req.Title,
		"description": req.Description,
		"category":    req.Category,
		"language":    req.Language,
		"tags":        req.Tags,
		"is_public":   isPublic,
		"is_mature":   isMature,
		"rtmp_url":    fmt.Sprintf("rtmp://localhost:%d", 1935),
		"stream_key":  streamKey,
		"ingest_key":  ingestKey,
		"ingest_hint": ingestHint,
		"hls_url":     fmt.Sprintf("%s/%s/index.m3u8", h.Config.MediaMTX.HLSBaseURL, streamKey),
	})
}

// =============================================================================
// UPDATE
// =============================================================================

// @Summary Update Stream
// @Description Update stream information (owner only)
// @Tags Streams
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param key path string true "Stream Key"
// @Param request body UpdateStreamRequest true "Stream update data"
// @Success 200 {object} map[string]interface{} "Stream updated successfully"
// @Failure 400 {object} map[string]interface{} "Validation error or no updates"
// @Failure 401 {object} map[string]interface{} "Unauthorized"
// @Failure 403 {object} map[string]interface{} "Forbidden - not stream owner"
// @Failure 404 {object} map[string]interface{} "Stream not found"
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /me/streams/{key} [put]
func (h *StreamsHandler) HandleUpdateStream(w http.ResponseWriter, r *http.Request) {
	streamKey := chi.URLParam(r, "key")
	if streamKey == "" {
		response.Error(w, "Stream key required", http.StatusBadRequest)
		return
	}

	userID := mw.GetUserID(r.Context())
	if userID == "" {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req UpdateStreamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if err := h.Validate.Struct(&req); err != nil {
		response.ValidationError(w, err)
		return
	}

	ctx := r.Context()

	// ownership
	var ownerID, streamID string
	if err := h.DB.QueryRow(ctx, "SELECT id, user_id FROM streams WHERE key = $1", streamKey).Scan(&streamID, &ownerID); err != nil {
		if err == sql.ErrNoRows {
			response.Error(w, "Stream not found", http.StatusNotFound)
		} else {
			h.Logger.Error("Failed to check stream ownership", "error", err)
			response.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}
	if ownerID != userID {
		response.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	updates := []string{}
	args := []interface{}{}
	arg := 1

	if req.Title != nil {
		updates = append(updates, fmt.Sprintf("title = $%d", arg))
		args = append(args, *req.Title)
		arg++
	}
	if req.Description != nil {
		updates = append(updates, fmt.Sprintf("description = $%d", arg))
		args = append(args, *req.Description)
		arg++
	}
	if req.Category != nil {
		updates = append(updates, fmt.Sprintf("category = $%d", arg))
		args = append(args, *req.Category)
		arg++
	}
	if req.Language != nil {
		updates = append(updates, fmt.Sprintf("language = $%d", arg))
		args = append(args, *req.Language)
		arg++
	}
	if req.Tags != nil {
		updates = append(updates, fmt.Sprintf("tags = $%d", arg))
		args = append(args, pq.Array(req.Tags))
		arg++
	}
	if req.IsPublic != nil {
		updates = append(updates, fmt.Sprintf("is_public = $%d", arg))
		args = append(args, *req.IsPublic)
		arg++
	}
	if req.IsMature != nil {
		updates = append(updates, fmt.Sprintf("is_mature = $%d", arg))
		args = append(args, *req.IsMature)
		arg++
	}
	if len(updates) == 0 {
		response.Error(w, "No updates provided", http.StatusBadRequest)
		return
	}

	updates = append(updates, fmt.Sprintf("updated_at = $%d", arg))
	args = append(args, time.Now())
	arg++

	// WHERE key
	args = append(args, streamKey)

	query := fmt.Sprintf(`
		UPDATE streams 
		SET %s 
		WHERE key = $%d
		RETURNING id, key, title, description, category, language, tags, is_public, is_mature, is_live
	`, strings.Join(updates, ", "), arg)

	var updated struct {
		ID          string   `json:"id"`
		Key         string   `json:"key"`
		Title       string   `json:"title"`
		Description *string  `json:"description"`
		Category    *string  `json:"category"`
		Language    *string  `json:"language"`
		Tags        []string `json:"tags"`
		IsPublic    bool     `json:"is_public"`
		IsMature    bool     `json:"is_mature"`
		IsLive      bool     `json:"is_live"`
	}

	if err := h.DB.QueryRow(ctx, query, args...).Scan(
		&updated.ID, &updated.Key, &updated.Title, &updated.Description, &updated.Category,
		&updated.Language, &updated.Tags, &updated.IsPublic, &updated.IsMature, &updated.IsLive,
	); err != nil {
		h.Logger.Error("Failed to update stream", "error", err)
		response.Error(w, "Failed to update stream", http.StatusInternalServerError)
		return
	}

	h.Logger.Info("Stream updated", "stream_key", streamKey, "user_id", userID)
	response.JSON(w, updated)
}

// =============================================================================
// DELETE
// =============================================================================

// @Summary Delete Stream
// @Description Delete a stream (owner only)
// @Tags Streams
// @Security BearerAuth
// @Param key path string true "Stream Key"
// @Success 200 {object} map[string]interface{} "Stream deleted successfully"
// @Failure 400 {object} map[string]interface{} "Stream key required"
// @Failure 401 {object} map[string]interface{} "Unauthorized"
// @Failure 403 {object} map[string]interface{} "Forbidden - not stream owner"
// @Failure 404 {object} map[string]interface{} "Stream not found"
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /me/streams/{key} [delete]
func (h *StreamsHandler) HandleDeleteStream(w http.ResponseWriter, r *http.Request) {
	streamKey := chi.URLParam(r, "key")
	if streamKey == "" {
		response.Error(w, "Stream key required", http.StatusBadRequest)
		return
	}

	userID := mw.GetUserID(r.Context())
	if userID == "" {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	ctx := r.Context()

	var ownerID, streamID string
	if err := h.DB.QueryRow(ctx, "SELECT id, user_id FROM streams WHERE key = $1", streamKey).Scan(&streamID, &ownerID); err != nil {
		if err == sql.ErrNoRows {
			response.Error(w, "Stream not found", http.StatusNotFound)
		} else {
			h.Logger.Error("Failed to check stream ownership", "error", err)
			response.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}
	if ownerID != userID {
		response.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Fermer le hub de chat si présent
	if h.ChatHubs != nil {
		if hub, ok := h.ChatHubs[streamKey]; ok && hub != nil {
			hub.Close()
			delete(h.ChatHubs, streamKey)
		}
	}

	if _, err := h.DB.Exec(ctx, "DELETE FROM streams WHERE key = $1", streamKey); err != nil {
		h.Logger.Error("Failed to delete stream", "error", err)
		response.Error(w, "Failed to delete stream", http.StatusInternalServerError)
		return
	}

	h.Logger.Info("Stream deleted", "stream_key", streamKey, "user_id", userID)
	response.JSON(w, map[string]string{"message": "Stream deleted successfully"})
}

// =============================================================================
// START / STOP
// =============================================================================

// @Summary Start Stream
// @Description Start a stream (set it as live)
// @Tags Streams
// @Security BearerAuth
// @Param key path string true "Stream Key"
// @Success 200 {object} map[string]interface{} "Stream started successfully"
// @Failure 400 {object} map[string]interface{} "Stream key required"
// @Failure 401 {object} map[string]interface{} "Unauthorized"
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /me/streams/{key}/start [post]
func (h *StreamsHandler) HandleStartStream(w http.ResponseWriter, r *http.Request) {
	streamKey := chi.URLParam(r, "key")
	if streamKey == "" {
		response.Error(w, "Stream key required", http.StatusBadRequest)
		return
	}
	userID := mw.GetUserID(r.Context())
	if userID == "" {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	ctx := r.Context()

	var ownerID string
	if err := h.DB.QueryRow(ctx, "SELECT user_id FROM streams WHERE key = $1", streamKey).Scan(&ownerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			response.Error(w, "Stream not found", http.StatusNotFound)
			return
		}
		h.Logger.Error("Failed to load stream owner", "stream_key", streamKey, "error", err)
		response.Error(w, "Failed to start stream", http.StatusInternalServerError)
		return
	}
	if ownerID != userID {
		response.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	commandTag, err := h.DB.Exec(ctx, `
		UPDATE streams 
		SET is_live = true, started_at = now(), updated_at = now()
		WHERE key = $1 AND user_id = $2
	`, streamKey, userID)
	if err != nil {
		h.Logger.Error("Failed to start stream", "error", err)
		response.Error(w, "Failed to start stream", http.StatusInternalServerError)
		return
	}
	if commandTag.RowsAffected() == 0 {
		h.Logger.Warn("Start stream had no effect", "stream_key", streamKey, "user_id", userID)
		response.Error(w, "Stream not found", http.StatusNotFound)
		return
	}

	h.Logger.Info("Stream started", "stream_key", streamKey, "user_id", userID)
	response.JSON(w, map[string]interface{}{
		"message": "Stream started",
		"key":     streamKey,
		"status":  "live",
	})
}

// @Summary Stop Stream
// @Description Stop a stream (set it as offline)
// @Tags Streams
// @Security BearerAuth
// @Param key path string true "Stream Key"
// @Success 200 {object} map[string]interface{} "Stream stopped successfully"
// @Failure 400 {object} map[string]interface{} "Stream key required"
// @Failure 401 {object} map[string]interface{} "Unauthorized"
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /me/streams/{key}/stop [post]
func (h *StreamsHandler) HandleStopStream(w http.ResponseWriter, r *http.Request) {
	streamKey := chi.URLParam(r, "key")
	if streamKey == "" {
		response.Error(w, "Stream key required", http.StatusBadRequest)
		return
	}
	userID := mw.GetUserID(r.Context())
	if userID == "" {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	ctx := r.Context()

	var ownerID string
	if err := h.DB.QueryRow(ctx, "SELECT user_id FROM streams WHERE key = $1", streamKey).Scan(&ownerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			response.Error(w, "Stream not found", http.StatusNotFound)
			return
		}
		h.Logger.Error("Failed to load stream owner", "stream_key", streamKey, "error", err)
		response.Error(w, "Failed to stop stream", http.StatusInternalServerError)
		return
	}
	if ownerID != userID {
		response.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	commandTag, err := h.DB.Exec(ctx, `
		UPDATE streams 
		SET is_live = false, current_viewers = 0, updated_at = now()
		WHERE key = $1 AND user_id = $2
	`, streamKey, userID)
	if err != nil {
		h.Logger.Error("Failed to stop stream", "error", err)
		response.Error(w, "Failed to stop stream", http.StatusInternalServerError)
		return
	}
	if commandTag.RowsAffected() == 0 {
		h.Logger.Warn("Stop stream had no effect", "stream_key", streamKey, "user_id", userID)
		response.Error(w, "Stream not found", http.StatusNotFound)
		return
	}

	// Fermer le hub de chat si présent
	if h.ChatHubs != nil {
		if hub, ok := h.ChatHubs[streamKey]; ok && hub != nil {
			hub.Close()
			delete(h.ChatHubs, streamKey)
		}
	}

	h.Logger.Info("Stream stopped", "stream_key", streamKey, "user_id", userID)
	response.JSON(w, map[string]interface{}{
		"message": "Stream stopped",
		"key":     streamKey,
		"status":  "offline",
	})
}

// =============================================================================
// ROTATE INGEST KEY
// =============================================================================

// @Summary Rotate Ingest Key
// @Description Generate a new ingest key for the stream (owner only)
// @Tags Streams
// @Security BearerAuth
// @Param key path string true "Stream Key"
// @Success 200 {object} map[string]interface{} "New ingest key returned"
// @Failure 400 {object} map[string]interface{} "Stream key required"
// @Failure 401 {object} map[string]interface{} "Unauthorized"
// @Failure 403 {object} map[string]interface{} "Forbidden"
// @Failure 404 {object} map[string]interface{} "Stream not found"
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /me/streams/{key}/rotate [post]
func (h *StreamsHandler) HandleRotateIngestKey(w http.ResponseWriter, r *http.Request) {
	streamKey := chi.URLParam(r, "key")
	if streamKey == "" {
		response.Error(w, "Stream key required", http.StatusBadRequest)
		return
	}
	userID := mw.GetUserID(r.Context())
	if userID == "" {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	ingestKey, err := generateStreamKey()
	if err != nil {
		h.Logger.Error("Failed to generate ingest key", "error", err)
		response.Error(w, "Failed to rotate ingest key", http.StatusInternalServerError)
		return
	}
	ingestHash, err := hashIngestKey(ingestKey)
	if err != nil {
		h.Logger.Error("Failed to hash ingest key", "error", err)
		response.Error(w, "Failed to rotate ingest key", http.StatusInternalServerError)
		return
	}
	ingestHint := ""
	if len(ingestKey) >= 4 {
		ingestHint = ingestKey[len(ingestKey)-4:]
	}

	ctx := r.Context()
	var streamID string
	err = h.DB.QueryRow(ctx, `
		UPDATE streams
		SET ingest_key_hash = $1, ingest_key_hint = $2, updated_at = now()
		WHERE key = $3 AND user_id = $4
		RETURNING id
	`, ingestHash, ingestHint, streamKey, userID).Scan(&streamID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			response.Error(w, "Stream not found", http.StatusNotFound)
		} else {
			h.Logger.Error("Failed to rotate ingest key", "stream_key", streamKey, "error", err)
			response.Error(w, "Failed to rotate ingest key", http.StatusInternalServerError)
		}
		return
	}

	h.Logger.Info("Ingest key rotated", "stream_key", streamKey, "user_id", userID)
	response.JSON(w, map[string]interface{}{
		"message":     "Ingest key rotated",
		"key":         streamKey,
		"ingest_key":  ingestKey,
		"ingest_hint": ingestHint,
		"stream_id":   streamID,
		"rotated_at":  time.Now().UTC(),
	})
}

// =============================================================================
// ANALYTICS (stub)
// =============================================================================

// @Summary Get Stream Analytics
// @Description Get analytics data for a specific stream (not implemented yet)
// @Tags Streams
// @Security BearerAuth
// @Param key path string true "Stream Key"
// @Success 200 {object} map[string]interface{} "Stream analytics"
// @Failure 501 {object} map[string]interface{} "Not implemented"
// @Router /streams/{key}/analytics [get]
func (h *StreamsHandler) HandleStreamAnalytics(w http.ResponseWriter, r *http.Request) {
	response.Error(w, "Not implemented", http.StatusNotImplemented)
}

// ============================================================================
// CATEGORIES (public)
// ============================================================================

// @Summary List Stream Categories
// @Description Get list of available stream categories
// @Tags Streams
// @Produce json
// @Success 200 {object} map[string]interface{} "List of categories"
// @Router /categories [get]
func (h *StreamsHandler) HandleListCategories(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, map[string]interface{}{
		"categories": []map[string]interface{}{
			{"id": 1, "name": "Just Chatting", "slug": "just-chatting"},
			{"id": 2, "name": "Gaming", "slug": "gaming"},
			{"id": 3, "name": "Music", "slug": "music"},
			{"id": 4, "name": "Art", "slug": "art"},
			{"id": 5, "name": "Tech", "slug": "tech"},
			{"id": 6, "name": "Education", "slug": "education"},
		},
	})
}
