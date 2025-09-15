// api/internal/http/handlers/auth.go
package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"golive/internal/auth"
	"golive/internal/config"
	mw "golive/internal/middleware"
	"golive/pkg/logger"
	"golive/pkg/response"
)

type AuthHandler struct {
	DB       *pgxpool.Pool
	Config   *config.Config
	Logger   *logger.Logger
	Validate *validator.Validate
}

type RegisterRequest struct {
	Email           string `json:"email" validate:"required,email" example:"user@example.com"`
	Username        string `json:"username" validate:"required,alphanum,min=3,max=20" example:"johndoe"`
	Password        string `json:"password" validate:"required,min=8" example:"password123"`
	ConfirmPassword string `json:"confirm_password" validate:"required,eqfield=Password" example:"password123"`
	DisplayName     string `json:"display_name" validate:"max=50" example:"John Doe"`
}

type LoginRequest struct {
	Email    string `json:"email" validate:"required,email" example:"user@example.com"`
	Password string `json:"password" validate:"required" example:"password123"`
}

func NewAuthHandler(db *pgxpool.Pool, cfg *config.Config, log *logger.Logger, validate *validator.Validate) *AuthHandler {
	return &AuthHandler{
		DB:       db,
		Config:   cfg,
		Logger:   log,
		Validate: validate,
	}
}

// @Summary User Registration
// @Description Register a new user account
// @Tags Auth
// @Accept json
// @Produce json
// @Param request body RegisterRequest true "Registration data"
// @Success 200 {object} map[string]interface{} "Registration successful"
// @Failure 400 {object} map[string]interface{} "Validation error"
// @Failure 409 {object} map[string]interface{} "Email or username already exists"
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /auth/register [post]
func (h *AuthHandler) HandleRegister(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if err := h.Validate.Struct(&req); err != nil {
		response.ValidationError(w, err)
		return
	}

	ctx := r.Context()

	// Vérifier si l'email existe déjà
	var exists bool
	err := h.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE email = $1)", req.Email).Scan(&exists)
	if err != nil {
		h.Logger.Error("Failed to check email existence", "error", err)
		response.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if exists {
		response.Error(w, "Email already registered", http.StatusConflict)
		return
	}

	// Vérifier si le username existe déjà
	err = h.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE username = $1)", req.Username).Scan(&exists)
	if err != nil {
		h.Logger.Error("Failed to check username existence", "error", err)
		response.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if exists {
		response.Error(w, "Username already taken", http.StatusConflict)
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		h.Logger.Error("Failed to hash password", "error", err)
		response.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	displayName := req.DisplayName
	if displayName == "" {
		displayName = req.Username
	}

	var userID string
	err = h.DB.QueryRow(ctx, `
		INSERT INTO users (email, username, display_name, password_hash, created_at, updated_at)
		VALUES ($1, $2, $3, $4, now(), now())
		RETURNING id
	`, req.Email, req.Username, displayName, string(hashedPassword)).Scan(&userID)

	if err != nil {
		h.Logger.Error("Failed to create user", "error", err)
		response.Error(w, "Failed to create user", http.StatusInternalServerError)
		return
	}

	token, err := auth.Sign(userID, req.Username, req.Email, h.Config.JWT.Secret, h.Config.JWT.Expiration)
	if err != nil {
		h.Logger.Error("Failed to generate token", "error", err)
		response.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Définir le cookie sécurisé
	h.setAuthCookie(w, token)

	response.JSON(w, map[string]interface{}{
		"user": map[string]interface{}{
			"id":           userID,
			"email":        req.Email,
			"username":     req.Username,
			"display_name": displayName,
		},
		"success": true,
	})
}

// @Summary User Login
// @Description Authenticate user and get JWT token
// @Tags Auth
// @Accept json
// @Produce json
// @Param request body LoginRequest true "Login credentials"
// @Success 200 {object} map[string]interface{} "Login successful"
// @Failure 400 {object} map[string]interface{} "Validation error"
// @Failure 401 {object} map[string]interface{} "Invalid credentials"
// @Failure 403 {object} map[string]interface{} "Account deactivated"
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /auth/login [post]
func (h *AuthHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if err := h.Validate.Struct(&req); err != nil {
		response.ValidationError(w, err)
		return
	}

	ctx := r.Context()

	var userID, username, displayName, passwordHash string
	var isActive bool
	err := h.DB.QueryRow(ctx, `
		SELECT id, username, display_name, password_hash, is_active
		FROM users WHERE email = $1
	`, req.Email).Scan(&userID, &username, &displayName, &passwordHash, &isActive)

	if err != nil {
		response.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}

	if !isActive {
		response.Error(w, "Account is deactivated", http.StatusForbidden)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password)); err != nil {
		response.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}

	// Mettre à jour la dernière connexion
	h.DB.Exec(ctx, "UPDATE users SET last_login_at = now() WHERE id = $1", userID)

	token, err := auth.Sign(userID, username, req.Email, h.Config.JWT.Secret, h.Config.JWT.Expiration)
	if err != nil {
		h.Logger.Error("Failed to generate token", "error", err)
		response.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Définir le cookie sécurisé
	h.setAuthCookie(w, token)

	response.JSON(w, map[string]interface{}{
		"user": map[string]interface{}{
			"id":           userID,
			"email":        req.Email,
			"username":     username,
			"display_name": displayName,
		},
		"success": true,
	})
}

// @Summary User Logout
// @Description Logout user and clear authentication cookie
// @Tags Auth
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]interface{} "Logout successful"
// @Router /auth/logout [post]
func (h *AuthHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	// Supprimer le cookie
	h.clearAuthCookie(w)
	response.JSON(w, map[string]interface{}{
		"success": true,
		"message": "Logged out successfully",
	})
}

// @Summary Refresh JWT Token
// @Description Refresh JWT token using existing token from cookie
// @Tags Auth
// @Produce json
// @Success 200 {object} map[string]interface{} "Token refreshed successfully"
// @Failure 401 {object} map[string]interface{} "Invalid or missing token"
// @Failure 403 {object} map[string]interface{} "Account deactivated"
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /auth/refresh [post]
func (h *AuthHandler) HandleRefreshToken(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("auth_token")
	if err != nil {
		response.Error(w, "No refresh token provided", http.StatusUnauthorized)
		return
	}

	// Verify the existing token
	claims, err := auth.Verify(cookie.Value, h.Config.JWT.Secret)
	if err != nil {
		response.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	ctx := r.Context()

	// Get updated user info from database
	var username, email, displayName string
	var isActive bool
	err = h.DB.QueryRow(ctx, `
		SELECT username, email, display_name, is_active
		FROM users WHERE id = $1
	`, claims.UserID).Scan(&username, &email, &displayName, &isActive)

	if err != nil {
		response.Error(w, "User not found", http.StatusUnauthorized)
		return
	}

	if !isActive {
		response.Error(w, "Account is deactivated", http.StatusForbidden)
		return
	}

	// Generate new token
	newToken, err := auth.Sign(claims.UserID, username, email, h.Config.JWT.Secret, h.Config.JWT.Expiration)
	if err != nil {
		h.Logger.Error("Failed to generate refresh token", "error", err)
		response.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Set new cookie
	h.setAuthCookie(w, newToken)

	response.JSON(w, map[string]interface{}{
		"user": map[string]interface{}{
			"id":           claims.UserID,
			"email":        email,
			"username":     username,
			"display_name": displayName,
		},
		"success": true,
	})
}

// @Summary Get Current User
// @Description Get current authenticated user information
// @Tags Auth
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]interface{} "User information"
// @Failure 401 {object} map[string]interface{} "Unauthorized"
// @Failure 404 {object} map[string]interface{} "User not found"
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /auth/me [get]
func (h *AuthHandler) HandleGetCurrentUser(w http.ResponseWriter, r *http.Request) {
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
		Bio         *string   `json:"bio" example:"Content creator"`
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

// Helper functions pour les cookies
func (h *AuthHandler) setAuthCookie(w http.ResponseWriter, token string) {
	cookie := &http.Cookie{
		Name:     "auth_token",
		Value:    token,
		Path:     "/",
		MaxAge:   int(h.Config.JWT.Expiration.Seconds()),
		HttpOnly: true,
		Secure:   false, // Set to true in production
		SameSite: http.SameSiteLaxMode, // Lax pour Docker
	}
	http.SetCookie(w, cookie)
}

func (h *AuthHandler) clearAuthCookie(w http.ResponseWriter) {
	cookie := &http.Cookie{
		Name:     "auth_token",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
	}
	http.SetCookie(w, cookie)
}