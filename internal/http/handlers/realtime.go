// api/internal/http/handlers/realtime.go
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	mw "golive/internal/middleware"
	"golive/internal/realtime"
	"golive/pkg/logger"
	"golive/pkg/response"
)

type RealtimeHandler struct {
	server *realtime.RealtimeServer
	db     *pgxpool.Pool
	rdb    *redis.Client
	logger *logger.Logger
}

func NewRealtimeHandler(db *pgxpool.Pool, rdb *redis.Client, logger *logger.Logger) *RealtimeHandler {
	server := realtime.NewRealtimeServer(db, rdb, logger)

	// Démarrer le serveur en arrière-plan
	go server.Run()

	return &RealtimeHandler{
		server: server,
		db:     db,
		rdb:    rdb,
		logger: logger,
	}
}

// HandleWebSocket gère les connexions WebSocket
// @Summary Realtime WebSocket connection
// @Description Etablit une connexion WebSocket pour le serveur temps réel. L'authentification par jeton est optionnelle.
// @Tags Realtime
// @Param Authorization header string false "Bearer <token>"
// @Success 101 {string} string "Connexion WebSocket acceptée"
// @Failure 400 {object} response.ErrorResponse
// @Router /ws [get]
func (h *RealtimeHandler) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	userInfo, err := h.buildUserInfo(r.Context())
	if err != nil {
		response.Error(w, "Failed to prepare user info", http.StatusInternalServerError)
		return
	}

	// Upgrade vers WebSocket
	conn, err := h.server.UpgradeConnection(w, r)
	if err != nil {
		h.logger.Error("Failed to upgrade WebSocket", "error", err)
		response.Error(w, "Failed to upgrade connection", http.StatusBadRequest)
		return
	}

	// Créer le client
	client := h.server.CreateClient(conn, userInfo)

	// Enregistrer et démarrer les pumps
	h.server.RegisterClient(client)
	go client.WritePump()
	go client.ReadPump()
}

// HandlePublishMessage publie un message via REST API
// @Summary Publier un message sur un channel
// @Description Publie un message pour tous les clients abonnés au channel spécifié.
// @Tags Realtime
// @Security BearerAuth
// @Param channel path string true "Channel ID (namespace:identifiant)"
// @Param request body PublishRequest true "Payload du message"
// @Success 200 {object} PublishResponse
// @Failure 400 {object} response.ErrorResponse
// @Failure 401 {object} response.ErrorResponse
// @Failure 500 {object} response.ErrorResponse
// @Router /realtime/channels/{channel}/publish [post]
func (h *RealtimeHandler) HandlePublishMessage(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channel")
	if channelID == "" {
		response.Error(w, "Channel ID required", http.StatusBadRequest)
		return
	}

	// Valider le format du channel
	if err := realtime.ValidateChannelID(channelID); err != nil {
		response.Error(w, "Invalid channel ID: "+err.Error(), http.StatusBadRequest)
		return
	}

	var req PublishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	userID := mw.GetUserID(r.Context())
	if userID == "" {
		response.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	// Récupérer les infos utilisateur
	user, err := h.getUserInfo(userID)
	if err != nil {
		response.Error(w, "Failed to get user info", http.StatusInternalServerError)
		return
	}

	if err := h.server.EnsurePublishAllowed(channelID, user); err != nil {
		h.handleRealtimeError(w, err)
		return
	}

	// Publier le message
	message := &realtime.Message{
		ID:        h.generateMessageID(),
		Type:      realtime.MessageTypeChat,
		Channel:   channelID,
		Data:      req.Data,
		User:      user,
		Timestamp: time.Now(),
	}

	if err := h.server.PublishMessage(message); err != nil {
		response.Error(w, "Failed to publish message: "+err.Error(), http.StatusInternalServerError)
		return
	}

	response.JSON(w, PublishResponse{
		MessageID: message.ID,
		Published: true,
		Timestamp: message.Timestamp,
	})
}

// HandleGetHistory récupère l'historique d'un channel
// @Summary Récupérer l'historique d'un channel
// @Description Retourne les derniers messages d'un channel temps réel.
// @Tags Realtime
// @Security BearerAuth
// @Param channel path string true "Channel ID (namespace:identifiant)"
// @Param limit query int false "Nombre maximum de messages" maximum(1000)
// @Param since query string false "Identifiant du message à partir duquel récupérer"
// @Success 200 {object} HistoryResponse
// @Failure 400 {object} response.ErrorResponse
// @Failure 404 {object} response.ErrorResponse
// @Router /realtime/channels/{channel}/history [get]
func (h *RealtimeHandler) HandleGetHistory(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channel")
	if channelID == "" {
		response.Error(w, "Channel ID required", http.StatusBadRequest)
		return
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 1000 {
			limit = parsed
		}
	}

	since := r.URL.Query().Get("since")

	history, err := h.server.GetChannelHistory(channelID, limit, since)
	if err != nil {
		response.Error(w, "Channel not found", http.StatusNotFound)
		return
	}

	response.JSON(w, HistoryResponse{
		Channel:  channelID,
		Messages: history,
		Total:    len(history),
		Limit:    limit,
		Since:    since,
	})
}

// HandleGetPresence récupère la présence d'un channel
// @Summary Récupérer la présence d'un channel
// @Description Retourne la liste des utilisateurs actuellement connectés sur le channel.
// @Tags Realtime
// @Security BearerAuth
// @Param channel path string true "Channel ID (namespace:identifiant)"
// @Success 200 {object} PresenceResponse
// @Failure 400 {object} response.ErrorResponse
// @Failure 404 {object} response.ErrorResponse
// @Router /realtime/channels/{channel}/presence [get]
func (h *RealtimeHandler) HandleGetPresence(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channel")
	if channelID == "" {
		response.Error(w, "Channel ID required", http.StatusBadRequest)
		return
	}

	presence, err := h.server.GetChannelPresence(channelID)
	if err != nil {
		response.Error(w, "Channel not found", http.StatusNotFound)
		return
	}

	response.JSON(w, PresenceResponse{
		Channel:  channelID,
		Presence: presence,
		Count:    len(presence),
	})
}

// HandleGetChannelInfo récupère les infos d'un channel
// @Summary Récupérer les informations d'un channel
// @Description Retourne la configuration et les statistiques instantanées du channel.
// @Tags Realtime
// @Security BearerAuth
// @Param channel path string true "Channel ID (namespace:identifiant)"
// @Success 200 {object} realtime.ChannelInfo
// @Failure 400 {object} response.ErrorResponse
// @Failure 404 {object} response.ErrorResponse
// @Router /realtime/channels/{channel} [get]
func (h *RealtimeHandler) HandleGetChannelInfo(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channel")
	if channelID == "" {
		response.Error(w, "Channel ID required", http.StatusBadRequest)
		return
	}

	info, err := h.server.GetChannelInfo(channelID)
	if err != nil {
		response.Error(w, "Channel not found", http.StatusNotFound)
		return
	}

	response.JSON(w, info)
}

// HandleDeleteMessage supprime un message (modération)
// @Summary Supprimer un message d'un channel
// @Description Supprime un message spécifique et informe les clients connectés.
// @Tags Realtime
// @Security BearerAuth
// @Param channel path string true "Channel ID (namespace:identifiant)"
// @Param messageID path string true "Identifiant du message"
// @Success 200 {object} ActionResponse
// @Failure 401 {object} response.ErrorResponse
// @Failure 403 {object} response.ErrorResponse
// @Failure 500 {object} response.ErrorResponse
// @Router /realtime/channels/{channel}/messages/{messageID} [delete]
func (h *RealtimeHandler) HandleDeleteMessage(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channel")
	messageID := chi.URLParam(r, "messageID")

	userID := mw.GetUserID(r.Context())
	if userID == "" {
		response.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	// Vérifier les permissions (propriétaire du stream ou modérateur)
	if !h.canModerateChannel(userID, channelID) {
		response.Error(w, "Insufficient permissions", http.StatusForbidden)
		return
	}

	// Supprimer le message
	if err := h.server.DeleteMessage(channelID, messageID); err != nil {
		response.Error(w, "Failed to delete message", http.StatusInternalServerError)
		return
	}

	// Diffuser l'événement de suppression
	deleteEvent := &realtime.Message{
		ID:      h.generateMessageID(),
		Type:    realtime.MessageTypeSystem,
		Channel: channelID,
		Data: map[string]interface{}{
			"action":     "message_deleted",
			"message_id": messageID,
			"deleted_by": userID,
		},
		Timestamp: time.Now(),
	}

	h.server.PublishMessage(deleteEvent)

	response.JSON(w, ActionResponse{Message: "Message deleted"})
}

// HandleTimeoutUser applique un timeout sur un utilisateur
// @Summary Timeout un utilisateur
// @Description Applique un timeout temporaire sur un utilisateur pour le channel cible.
// @Tags Realtime
// @Security BearerAuth
// @Param channel path string true "Channel ID (namespace:identifiant)"
// @Param userID path string true "ID utilisateur ciblé"
// @Param request body TimeoutRequest true "Paramètres du timeout"
// @Success 200 {object} TimeoutResponse
// @Failure 401 {object} response.ErrorResponse
// @Failure 403 {object} response.ErrorResponse
// @Failure 500 {object} response.ErrorResponse
// @Router /realtime/channels/{channel}/users/{userID}/timeout [post]
func (h *RealtimeHandler) HandleTimeoutUser(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channel")
	targetUserID := chi.URLParam(r, "userID")

	moderatorID := mw.GetUserID(r.Context())
	if moderatorID == "" {
		response.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	if !h.canModerateChannel(moderatorID, channelID) {
		response.Error(w, "Insufficient permissions", http.StatusForbidden)
		return
	}

	var req TimeoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	duration := time.Duration(req.Duration) * time.Second
	if duration <= 0 {
		duration = 5 * time.Minute
	}

	if err := h.server.SaveTimeout(r.Context(), channelID, targetUserID, req.Reason, moderatorID, duration); err != nil {
		h.handleRealtimeError(w, err)
		return
	}

	timeoutEvent := &realtime.Message{
		ID:      h.generateMessageID(),
		Type:    realtime.MessageTypeSystem,
		Channel: channelID,
		Data: map[string]interface{}{
			"action":     "user_timeout",
			"user_id":    targetUserID,
			"reason":     req.Reason,
			"timeout_by": moderatorID,
			"duration":   int(duration.Seconds()),
		},
		Timestamp: time.Now(),
	}

	if err := h.server.PublishMessage(timeoutEvent); err != nil {
		h.handleRealtimeError(w, err)
		return
	}

	response.JSON(w, TimeoutResponse{
		Message:   "User timed out",
		Duration:  int(duration.Seconds()),
		Reason:    req.Reason,
		ExpiresAt: time.Now().Add(duration),
	})
}

// HandleBanUser applique un ban permanent
// @Summary Bannir un utilisateur
// @Description Empêche définitivement un utilisateur de publier sur le channel.
// @Tags Realtime
// @Security BearerAuth
// @Param channel path string true "Channel ID (namespace:identifiant)"
// @Param userID path string true "ID utilisateur ciblé"
// @Param request body BanRequest true "Motif du ban"
// @Success 200 {object} BanResponse
// @Failure 401 {object} response.ErrorResponse
// @Failure 403 {object} response.ErrorResponse
// @Failure 500 {object} response.ErrorResponse
// @Router /realtime/channels/{channel}/users/{userID}/ban [post]
func (h *RealtimeHandler) HandleBanUser(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channel")
	targetUserID := chi.URLParam(r, "userID")

	moderatorID := mw.GetUserID(r.Context())
	if moderatorID == "" {
		response.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	if !h.canModerateChannel(moderatorID, channelID) {
		response.Error(w, "Insufficient permissions", http.StatusForbidden)
		return
	}

	var req BanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req.Reason = ""
	}

	if err := h.server.SaveBan(r.Context(), channelID, targetUserID, req.Reason, moderatorID); err != nil {
		h.handleRealtimeError(w, err)
		return
	}

	banEvent := &realtime.Message{
		ID:      h.generateMessageID(),
		Type:    realtime.MessageTypeSystem,
		Channel: channelID,
		Data: map[string]interface{}{
			"action":    "user_banned",
			"user_id":   targetUserID,
			"reason":    req.Reason,
			"banned_by": moderatorID,
		},
		Timestamp: time.Now(),
	}

	if err := h.server.PublishMessage(banEvent); err != nil {
		h.handleRealtimeError(w, err)
		return
	}

	response.JSON(w, BanResponse{
		Message: "User banned",
		Reason:  req.Reason,
	})
}

// HandleUnbanUser retire un ban existant
// @Summary Débannir un utilisateur
// @Description Retire un ban précédemment appliqué et notifie le channel.
// @Tags Realtime
// @Security BearerAuth
// @Param channel path string true "Channel ID (namespace:identifiant)"
// @Param userID path string true "ID utilisateur ciblé"
// @Success 200 {object} ActionResponse
// @Failure 401 {object} response.ErrorResponse
// @Failure 403 {object} response.ErrorResponse
// @Failure 500 {object} response.ErrorResponse
// @Router /realtime/channels/{channel}/users/{userID}/ban [delete]
func (h *RealtimeHandler) HandleUnbanUser(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channel")
	targetUserID := chi.URLParam(r, "userID")

	moderatorID := mw.GetUserID(r.Context())
	if moderatorID == "" {
		response.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	if !h.canModerateChannel(moderatorID, channelID) {
		response.Error(w, "Insufficient permissions", http.StatusForbidden)
		return
	}

	if err := h.server.RemoveBan(r.Context(), channelID, targetUserID); err != nil {
		h.handleRealtimeError(w, err)
		return
	}

	unbanEvent := &realtime.Message{
		ID:      h.generateMessageID(),
		Type:    realtime.MessageTypeSystem,
		Channel: channelID,
		Data: map[string]interface{}{
			"action":      "user_unbanned",
			"user_id":     targetUserID,
			"unbanned_by": moderatorID,
		},
		Timestamp: time.Now(),
	}

	if err := h.server.PublishMessage(unbanEvent); err != nil {
		h.handleRealtimeError(w, err)
		return
	}

	response.JSON(w, ActionResponse{Message: "User unbanned"})
}

// HandleClearChannel vide un channel (modération)
// @Summary Vider un channel
// @Description Efface l'historique d'un channel et avertit les clients.
// @Tags Realtime
// @Security BearerAuth
// @Param channel path string true "Channel ID (namespace:identifiant)"
// @Success 200 {object} ActionResponse
// @Failure 403 {object} response.ErrorResponse
// @Failure 500 {object} response.ErrorResponse
// @Router /realtime/channels/{channel}/clear [post]
func (h *RealtimeHandler) HandleClearChannel(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channel")
	userID := mw.GetUserID(r.Context())

	if !h.canModerateChannel(userID, channelID) {
		response.Error(w, "Insufficient permissions", http.StatusForbidden)
		return
	}

	if err := h.server.ClearChannel(channelID); err != nil {
		response.Error(w, "Failed to clear channel", http.StatusInternalServerError)
		return
	}

	// Diffuser l'événement de nettoyage
	clearEvent := &realtime.Message{
		ID:      h.generateMessageID(),
		Type:    realtime.MessageTypeSystem,
		Channel: channelID,
		Data: map[string]interface{}{
			"action":     "channel_cleared",
			"cleared_by": userID,
		},
		Timestamp: time.Now(),
	}

	h.server.PublishMessage(clearEvent)

	response.JSON(w, ActionResponse{Message: "Channel cleared"})
}

// HandleUpdateChannelSettings met à jour la configuration d'un channel
// @Summary Mettre à jour les paramètres d'un channel
// @Description Ajuste les limites et règles d'accès d'un channel temps réel.
// @Tags Realtime
// @Security BearerAuth
// @Param channel path string true "Channel ID (namespace:identifiant)"
// @Param request body ChannelSettingsRequest true "Paramètres du channel"
// @Success 200 {object} realtime.ChannelSettings
// @Failure 400 {object} response.ErrorResponse
// @Failure 403 {object} response.ErrorResponse
// @Router /realtime/channels/{channel}/settings [put]
func (h *RealtimeHandler) HandleUpdateChannelSettings(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channel")
	if channelID == "" {
		response.Error(w, "Channel ID required", http.StatusBadRequest)
		return
	}

	var req ChannelSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	parts := strings.SplitN(channelID, ":", 2)
	if len(parts) < 2 {
		response.Error(w, "Invalid channel ID", http.StatusBadRequest)
		return
	}

	channel := h.server.GetOrCreateChannel(channelID, parts[0])
	current := channel.GetSettings()
	updates := &realtime.ChannelSettings{
		MaxClients:    current.MaxClients,
		MaxHistory:    current.MaxHistory,
		TTL:           current.TTL,
		RequireAuth:   current.RequireAuth,
		AllowPublish:  current.AllowPublish,
		AllowPresence: current.AllowPresence,
		Private:       current.Private,
		AllowedRoles:  append([]string(nil), current.AllowedRoles...),
		ProxyEndpoint: current.ProxyEndpoint,
	}

	if req.MaxClients != nil {
		updates.MaxClients = *req.MaxClients
	}
	if req.MaxHistory != nil {
		updates.MaxHistory = *req.MaxHistory
	}
	if req.TTLSeconds != nil {
		updates.TTL = time.Duration(*req.TTLSeconds) * time.Second
	}
	if req.RequireAuth != nil {
		updates.RequireAuth = *req.RequireAuth
	}
	if req.AllowPublish != nil {
		updates.AllowPublish = *req.AllowPublish
	}
	if req.AllowPresence != nil {
		updates.AllowPresence = *req.AllowPresence
	}
	if req.Private != nil {
		updates.Private = *req.Private
	}
	if req.AllowedRoles != nil {
		updates.AllowedRoles = req.AllowedRoles
	}
	if req.ProxyEndpoint != nil {
		updates.ProxyEndpoint = *req.ProxyEndpoint
	}

	settings, err := h.server.UpdateChannelSettings(channelID, updates)
	if err != nil {
		h.handleRealtimeError(w, err)
		return
	}

	response.JSON(w, settings)
}

// HandleGrantChannelAccess autorise des utilisateurs sur un channel privé
// @Summary Autoriser l'accès à un channel
// @Description Ajoute une liste d'utilisateurs autorisés sur un channel privé.
// @Tags Realtime
// @Security BearerAuth
// @Param channel path string true "Channel ID (namespace:identifiant)"
// @Param request body ChannelAccessRequest true "Liste des utilisateurs"
// @Success 200 {object} ActionResponse
// @Failure 400 {object} response.ErrorResponse
// @Failure 500 {object} response.ErrorResponse
// @Router /realtime/channels/{channel}/access [post]
func (h *RealtimeHandler) HandleGrantChannelAccess(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channel")
	if channelID == "" {
		response.Error(w, "Channel ID required", http.StatusBadRequest)
		return
	}

	var req ChannelAccessRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if err := h.server.GrantChannelAccess(r.Context(), channelID, req.UserIDs); err != nil {
		h.handleRealtimeError(w, err)
		return
	}

	response.JSON(w, ActionResponse{Message: "Access granted"})
}

// HandleRevokeChannelAccess révoque l'accès d'un utilisateur
// @Summary Révoquer l'accès d'un utilisateur
// @Description Supprime l'accès d'un utilisateur à un channel privé.
// @Tags Realtime
// @Security BearerAuth
// @Param channel path string true "Channel ID (namespace:identifiant)"
// @Param userID path string true "ID utilisateur"
// @Success 200 {object} ActionResponse
// @Failure 400 {object} response.ErrorResponse
// @Failure 500 {object} response.ErrorResponse
// @Router /realtime/channels/{channel}/access/{userID} [delete]
func (h *RealtimeHandler) HandleRevokeChannelAccess(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channel")
	userID := chi.URLParam(r, "userID")
	if channelID == "" || userID == "" {
		response.Error(w, "Channel ID and user ID required", http.StatusBadRequest)
		return
	}

	if err := h.server.RevokeChannelAccess(r.Context(), channelID, userID); err != nil {
		h.handleRealtimeError(w, err)
		return
	}

	response.JSON(w, ActionResponse{Message: "Access revoked"})
}

// HandleSyncPresence remplace la présence par une source externe
// @Summary Synchroniser la présence d'un channel
// @Description Remplace la présence en mémoire par la liste fournie (usage déporté).
// @Tags Realtime
// @Security BearerAuth
// @Param channel path string true "Channel ID (namespace:identifiant)"
// @Param request body PresenceSyncRequest true "Liste de présence"
// @Success 200 {object} ActionResponse
// @Failure 400 {object} response.ErrorResponse
// @Failure 500 {object} response.ErrorResponse
// @Router /realtime/channels/{channel}/presence/sync [post]
func (h *RealtimeHandler) HandleSyncPresence(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channel")
	if channelID == "" {
		response.Error(w, "Channel ID required", http.StatusBadRequest)
		return
	}

	var req PresenceSyncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if err := h.server.SyncPresence(r.Context(), channelID, req.Entries); err != nil {
		h.handleRealtimeError(w, err)
		return
	}

	response.JSON(w, ActionResponse{Message: "Presence synced"})
}

// HandleRPC exécute une commande RPC sur le serveur realtime
// @Summary Appeler un RPC realtime
// @Description Exécute une méthode RPC exposée par le serveur realtime.
// @Tags Realtime
// @Security BearerAuth
// @Param request body RPCRequest true "Requête RPC"
// @Success 200 {object} RPCResponse
// @Failure 400 {object} response.ErrorResponse
// @Failure 500 {object} response.ErrorResponse
// @Router /realtime/rpc [post]
func (h *RealtimeHandler) HandleRPC(w http.ResponseWriter, r *http.Request) {
	var req RPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Method == "" {
		response.Error(w, "Method required", http.StatusBadRequest)
		return
	}

	var user *realtime.UserInfo
	if userID := mw.GetUserID(r.Context()); userID != "" {
		info, err := h.getUserInfo(userID)
		if err == nil {
			user = info
		}
	}

	result, err := h.server.ExecuteRPC(r.Context(), req.Method, user, req.Params)
	resp := RPCResponse{
		Method: req.Method,
	}
	if err != nil {
		resp.Error = err.Error()
	} else {
		resp.Result = result
	}

	response.JSON(w, resp)
}

// HandleGetChannelList liste tous les channels
// @Summary Lister les channels actifs
// @Description Retourne la liste complète des channels en mémoire.
// @Tags Realtime
// @Security BearerAuth
// @Success 200 {object} ChannelListResponse
// @Failure 401 {object} response.ErrorResponse
// @Router /realtime/channels [get]
func (h *RealtimeHandler) HandleGetChannelList(w http.ResponseWriter, r *http.Request) {
	// Seulement pour les admins
	userID := mw.GetUserID(r.Context())
	if userID == "" {
		response.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	channels := h.server.GetChannelList()
	response.JSON(w, ChannelListResponse{
		Channels: channels,
		Total:    len(channels),
	})
}

// HandleGetStats retourne les statistiques du serveur
// @Summary Obtenir les statistiques du serveur realtime
// @Description Retourne des informations agrégées sur les channels et clients connectés.
// @Tags Realtime
// @Security BearerAuth
// @Success 200 {object} realtime.ServerStats
// @Router /realtime/stats [get]
func (h *RealtimeHandler) HandleGetStats(w http.ResponseWriter, r *http.Request) {
	stats := h.server.GetStats()
	response.JSON(w, stats)
}

// HandleBroadcastToChannel diffuse un message système à un channel
// @Summary Diffuser un message système sur un channel
// @Description Envoie un message système à tous les clients abonnés.
// @Tags Realtime
// @Security BearerAuth
// @Param channel path string true "Channel ID (namespace:identifiant)"
// @Param request body BroadcastRequest true "Payload système"
// @Success 200 {object} BroadcastResponse
// @Failure 403 {object} response.ErrorResponse
// @Failure 500 {object} response.ErrorResponse
// @Router /realtime/channels/{channel}/broadcast [post]
func (h *RealtimeHandler) HandleBroadcastToChannel(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channel")
	userID := mw.GetUserID(r.Context())

	if !h.canModerateChannel(userID, channelID) {
		response.Error(w, "Insufficient permissions", http.StatusForbidden)
		return
	}

	var req BroadcastRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	message := &realtime.Message{
		ID:        h.generateMessageID(),
		Type:      realtime.MessageTypeSystem,
		Channel:   channelID,
		Data:      req.Data,
		Timestamp: time.Now(),
	}

	if err := h.server.BroadcastToChannel(channelID, message); err != nil {
		response.Error(w, "Failed to broadcast message", http.StatusInternalServerError)
		return
	}

	response.JSON(w, BroadcastResponse{
		MessageID: message.ID,
		Broadcast: true,
	})
}

// ============================================================================
// DTOs
// ============================================================================

type PublishRequest struct {
	Data map[string]interface{} `json:"data"`
}

type BroadcastRequest struct {
	Data map[string]interface{} `json:"data"`
}

type PublishResponse struct {
	MessageID string    `json:"message_id"`
	Published bool      `json:"published"`
	Timestamp time.Time `json:"timestamp"`
}

type BroadcastResponse struct {
	MessageID string `json:"message_id"`
	Broadcast bool   `json:"broadcast"`
}

type TimeoutRequest struct {
	Duration int    `json:"duration"`
	Reason   string `json:"reason"`
}

type BanRequest struct {
	Reason string `json:"reason"`
}

type HistoryResponse struct {
	Channel  string              `json:"channel"`
	Messages []*realtime.Message `json:"messages"`
	Total    int                 `json:"total"`
	Limit    int                 `json:"limit"`
	Since    string              `json:"since,omitempty"`
}

type PresenceResponse struct {
	Channel  string                            `json:"channel"`
	Presence map[string]*realtime.PresenceInfo `json:"presence"`
	Count    int                               `json:"count"`
}

type ChannelListResponse struct {
	Channels []*realtime.ChannelInfo `json:"channels"`
	Total    int                     `json:"total"`
}

type ActionResponse struct {
	Message string `json:"message"`
}

type TimeoutResponse struct {
	Message   string    `json:"message"`
	Duration  int       `json:"duration"`
	Reason    string    `json:"reason"`
	ExpiresAt time.Time `json:"expires_at"`
}

type BanResponse struct {
	Message string `json:"message"`
	Reason  string `json:"reason"`
}

type ChannelSettingsRequest struct {
	MaxClients    *int     `json:"max_clients"`
	MaxHistory    *int     `json:"max_history"`
	TTLSeconds    *int64   `json:"ttl_seconds"`
	RequireAuth   *bool    `json:"require_auth"`
	AllowPublish  *bool    `json:"allow_publish"`
	AllowPresence *bool    `json:"allow_presence"`
	Private       *bool    `json:"private"`
	AllowedRoles  []string `json:"allowed_roles"`
	ProxyEndpoint *string  `json:"proxy_endpoint"`
}

type ChannelAccessRequest struct {
	UserIDs []string `json:"user_ids"`
}

type PresenceSyncRequest struct {
	Entries []*realtime.PresenceInfo `json:"entries"`
}

type RPCRequest struct {
	Method string                 `json:"method"`
	Params map[string]interface{} `json:"params"`
}

type RPCResponse struct {
	Method string                 `json:"method"`
	Result map[string]interface{} `json:"result,omitempty"`
	Error  string                 `json:"error,omitempty"`
}

// ============================================================================
// HELPER METHODS
// ============================================================================

func (h *RealtimeHandler) handleRealtimeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, realtime.ErrStreamNotFound):
		response.Error(w, "Channel not found", http.StatusNotFound)
	case errors.Is(err, realtime.ErrChatDisabled):
		response.Error(w, "Chat disabled for stream", http.StatusForbidden)
	case errors.Is(err, realtime.ErrUserBanned):
		response.Error(w, err.Error(), http.StatusForbidden)
	case errors.Is(err, realtime.ErrUserTimedOut):
		response.Error(w, err.Error(), http.StatusTooManyRequests)
	case errors.Is(err, realtime.ErrUserNotAuthorized):
		response.Error(w, err.Error(), http.StatusForbidden)
	case errors.Is(err, realtime.ErrInvalidChannelID):
		response.Error(w, err.Error(), http.StatusBadRequest)
	default:
		h.logger.Error("Realtime handler error", "error", err)
		response.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func (h *RealtimeHandler) buildUserInfo(ctx context.Context) (*realtime.UserInfo, error) {
	if claims, ok := mw.GetUserClaims(ctx); ok {
		user, err := h.getUserInfo(claims.UserID)
		if err != nil {
			return &realtime.UserInfo{
				ID:          claims.UserID,
				Username:    claims.Username,
				DisplayName: claims.Username,
			}, nil
		}
		return user, nil
	}

	anonID := h.generateAnonymousID()
	return &realtime.UserInfo{
		ID:          "anon_" + anonID,
		Username:    "Anonymous_" + anonID,
		DisplayName: "Anonymous User",
	}, nil
}

func (h *RealtimeHandler) getUserInfo(userID string) (*realtime.UserInfo, error) {
	ctx := context.Background()

	var user realtime.UserInfo
	err := h.db.QueryRow(ctx, `
		SELECT id, username, display_name, COALESCE(avatar_url, ''), COALESCE(role, 'user')
		FROM users WHERE id = $1
	`, userID).Scan(&user.ID, &user.Username, &user.DisplayName, &user.Avatar, &user.Role)

	if err != nil {
		return nil, err
	}

	return &user, nil
}

func (h *RealtimeHandler) canModerateChannel(userID, channelID string) bool {
	// Parse channel: "chat:stream_123" -> vérifier si user possède stream_123
	parts := strings.SplitN(channelID, ":", 2)
	if len(parts) != 2 {
		return false
	}

	namespace := parts[0]
	identifier := parts[1]

	switch namespace {
	case "chat":
		// Pour les chats de stream, vérifier la propriété du stream
		return h.isStreamOwner(userID, identifier)
	case "stream":
		// Pour les channels de stream, vérifier la propriété
		return h.isStreamOwner(userID, identifier)
	default:
		// Pour les autres namespaces, seuls les admins peuvent modérer
		return h.isAdmin(userID)
	}
}

func (h *RealtimeHandler) isStreamOwner(userID, streamKey string) bool {
	ctx := context.Background()
	var isOwner bool
	err := h.db.QueryRow(ctx, `
		SELECT user_id = $1 FROM streams WHERE key = $2
	`, userID, streamKey).Scan(&isOwner)

	return err == nil && isOwner
}

func (h *RealtimeHandler) isAdmin(userID string) bool {
	ctx := context.Background()
	var role string
	err := h.db.QueryRow(ctx, `
		SELECT role FROM users WHERE id = $1
	`, userID).Scan(&role)

	return err == nil && (role == "admin" || role == "moderator")
}

func (h *RealtimeHandler) generateMessageID() string {
	return "msg_" + time.Now().Format("20060102150405") + "_" + h.randomString(8)
}

func (h *RealtimeHandler) generateAnonymousID() string {
	return time.Now().Format("150405") + h.randomString(6)
}

func (h *RealtimeHandler) randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[time.Now().UnixNano()%int64(len(charset))]
	}
	return string(b)
}
