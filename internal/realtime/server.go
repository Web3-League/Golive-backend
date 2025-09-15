// api/internal/realtime/server.go
package realtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"golive/pkg/logger"
)

// RealtimeServer gère tous les WebSockets et channels
type RealtimeServer struct {
	channels   map[string]*Channel
	clients    map[*Client]bool
	register   chan *Client
	unregister chan *Client
	broadcast  chan *Message

	db            *pgxpool.Pool
	rdb           *redis.Client
	logger        *logger.Logger
	redisManager  *RedisManager
	rpcHandlers   map[string]RPCHandler
	bans          map[string]map[string]*BanInfo
	timeouts      map[string]map[string]*TimeoutInfo
	channelAccess map[string]map[string]bool
	config        *Config

	upgrader websocket.Upgrader
	mu       sync.RWMutex
}

type RPCHandler func(ctx context.Context, user *UserInfo, params map[string]interface{}) (map[string]interface{}, error)

// NewRealtimeServer crée une nouvelle instance du serveur
func NewRealtimeServer(db *pgxpool.Pool, rdb *redis.Client, logger *logger.Logger) *RealtimeServer {
	manager := NewRedisManager(rdb, logger)
	s := &RealtimeServer{
		channels:      make(map[string]*Channel),
		clients:       make(map[*Client]bool),
		register:      make(chan *Client),
		unregister:    make(chan *Client),
		broadcast:     make(chan *Message, 256),
		db:            db,
		rdb:           rdb,
		logger:        logger,
		redisManager:  manager,
		rpcHandlers:   make(map[string]RPCHandler),
		bans:          make(map[string]map[string]*BanInfo),
		timeouts:      make(map[string]map[string]*TimeoutInfo),
		channelAccess: make(map[string]map[string]bool),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				return true // En production, vérifier l'origine
			},
		},
	}

	s.loadStaticConfig()
	s.registerDefaultRPCHandlers()

	return s
}

// Run démarre la boucle principale du serveur
func (s *RealtimeServer) Run() {
	for {
		select {
		case client := <-s.register:
			s.handleRegister(client)

		case client := <-s.unregister:
			s.handleUnregister(client)

		case message := <-s.broadcast:
			s.handleBroadcast(message)
		}
	}
}

func (s *RealtimeServer) registerDefaultRPCHandlers() {
	s.RegisterRPCHandler("ping", func(ctx context.Context, user *UserInfo, params map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"pong": time.Now()}, nil
	})

	s.RegisterRPCHandler("stats", func(ctx context.Context, user *UserInfo, params map[string]interface{}) (map[string]interface{}, error) {
		stats := s.GetStats()
		return map[string]interface{}{
			"total_channels": stats.TotalChannels,
			"total_clients":  stats.TotalClients,
		}, nil
	})
}

func (s *RealtimeServer) loadStaticConfig() {
	path := os.Getenv("REALTIME_CONFIG_PATH")
	if path == "" {
		return
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		s.logger.Error("Failed to load realtime config", "path", path, "error", err)
		return
	}

	s.config = cfg
	s.logger.Info("Realtime config loaded", "path", path, "namespaces", len(cfg.Namespaces), "channels", len(cfg.Channels))
}

func (s *RealtimeServer) handleRegister(client *Client) {
	s.mu.Lock()
	s.clients[client] = true
	s.mu.Unlock()

	s.logger.Info("Client connected",
		"client_id", client.ID,
		"user_id", client.user.ID,
		"total_clients", len(s.clients))

	// Envoyer message de bienvenue
	welcome := &Message{
		ID:        generateID(),
		Type:      MessageTypeConnect,
		Data:      map[string]interface{}{"status": "connected", "client_id": client.ID},
		Timestamp: time.Now(),
	}
	client.Send(welcome)
}

func (s *RealtimeServer) handleUnregister(client *Client) {
	s.mu.Lock()
	if _, exists := s.clients[client]; exists {
		delete(s.clients, client)
		close(client.send)

		// Désabonner de tous les channels
		for channelID, channel := range client.channels {
			s.unsubscribeFromChannel(client, channelID, channel)
		}
	}
	s.mu.Unlock()

	s.logger.Info("Client disconnected",
		"client_id", client.ID,
		"total_clients", len(s.clients))
}

func (s *RealtimeServer) handleBroadcast(message *Message) {
	s.mu.RLock()
	channel := s.channels[message.Channel]
	s.mu.RUnlock()

	if channel == nil {
		return
	}

	// Ajouter à l'historique
	channel.AddToHistory(message)

	// Diffuser aux clients connectés au channel
	channel.mu.RLock()
	for client := range channel.clients {
		select {
		case client.send <- s.serializeMessage(message):
		default:
			close(client.send)
			delete(channel.clients, client)
		}
	}
	channel.mu.RUnlock()

	// Sauvegarder en DB si c'est un message chat
	if message.Type == MessageTypeChat {
		go s.saveMessage(message)
	}

	if channel.settings.ProxyEndpoint != "" {
		go s.forwardToProxy(channel.settings.ProxyEndpoint, message)
	}
}

// GetOrCreateChannel récupère ou crée un channel
func (s *RealtimeServer) GetOrCreateChannel(channelID, namespace string) *Channel {
	s.mu.Lock()
	if channel, exists := s.channels[channelID]; exists {
		s.mu.Unlock()
		return channel
	}

	settings := s.getDefaultSettings(namespace)
	channel := NewChannel(channelID, namespace, settings)
	s.channels[channelID] = channel
	s.mu.Unlock()

	s.logger.Info("Channel created",
		"channel_id", channelID,
		"namespace", namespace)

	s.applyChannelConfig(channelID, namespace, channel)

	return channel
}

func (s *RealtimeServer) getDefaultSettings(namespace string) *ChannelSettings {
	settings := &ChannelSettings{
		MaxClients:    100,
		MaxHistory:    20,
		TTL:           1 * time.Hour,
		RequireAuth:   true,
		AllowPublish:  false,
		AllowPresence: false,
	}

	switch namespace {
	case "chat":
		settings = &ChannelSettings{
			MaxClients:    1000,
			MaxHistory:    100,
			TTL:           24 * time.Hour,
			RequireAuth:   false,
			AllowPublish:  true,
			AllowPresence: true,
		}
	case "stream":
		settings = &ChannelSettings{
			MaxClients:    10000,
			MaxHistory:    50,
			TTL:           12 * time.Hour,
			RequireAuth:   false,
			AllowPublish:  false,
			AllowPresence: true,
		}
	}

	if s.config != nil {
		if nsCfg := s.config.namespaceConfig(namespace); nsCfg != nil {
			if nsCfg.MaxClients != nil {
				settings.MaxClients = *nsCfg.MaxClients
			}
			if nsCfg.MaxHistory != nil {
				settings.MaxHistory = *nsCfg.MaxHistory
			}
			if nsCfg.TTL != "" {
				settings.TTL = parseTTL(nsCfg.TTL, settings.TTL)
			}
			if nsCfg.RequireAuth != nil {
				settings.RequireAuth = *nsCfg.RequireAuth
			}
			if nsCfg.AllowPublish != nil {
				settings.AllowPublish = *nsCfg.AllowPublish
			}
			if nsCfg.AllowPresence != nil {
				settings.AllowPresence = *nsCfg.AllowPresence
			}
			if nsCfg.Private != nil {
				settings.Private = *nsCfg.Private
			}
			if len(nsCfg.AllowedRoles) > 0 {
				settings.AllowedRoles = append([]string(nil), nsCfg.AllowedRoles...)
			}
			if nsCfg.ProxyEndpoint != "" {
				settings.ProxyEndpoint = nsCfg.ProxyEndpoint
			}
		}
	}

	return settings
}

func (s *RealtimeServer) applyChannelConfig(channelID, namespace string, channel *Channel) {
	if s.config == nil {
		return
	}

	channelCfg := s.config.channelConfig(channelID, namespace)
	if channelCfg == nil {
		return
	}

	current := channel.GetSettings()

	if channelCfg.MaxClients != nil {
		current.MaxClients = *channelCfg.MaxClients
	}
	if channelCfg.MaxHistory != nil {
		current.MaxHistory = *channelCfg.MaxHistory
	}
	if channelCfg.TTL != "" {
		current.TTL = parseTTL(channelCfg.TTL, current.TTL)
	}
	if channelCfg.RequireAuth != nil {
		current.RequireAuth = *channelCfg.RequireAuth
	}
	if channelCfg.AllowPublish != nil {
		current.AllowPublish = *channelCfg.AllowPublish
	}
	if channelCfg.AllowPresence != nil {
		current.AllowPresence = *channelCfg.AllowPresence
	}
	if channelCfg.Private != nil {
		current.Private = *channelCfg.Private
	}
	if len(channelCfg.AllowedRoles) > 0 {
		current.AllowedRoles = append([]string(nil), channelCfg.AllowedRoles...)
	}
	if channelCfg.ProxyEndpoint != "" {
		current.ProxyEndpoint = channelCfg.ProxyEndpoint
	}

	channel.UpdateSettings(current)

	if len(channelCfg.AllowedUsers) > 0 {
		s.grantAccess(channelID, channelCfg.AllowedUsers)
	}
}

func (s *RealtimeServer) subscribeToChannel(client *Client, channelID string) error {
	parts := parseChannelID(channelID) // ex: "chat:stream_123" -> ["chat", "stream_123"]
	if len(parts) < 2 {
		return ErrInvalidChannelID
	}

	namespace := parts[0]
	identifier := parts[1]

	if namespace == "chat" {
		if err := s.ensureChatStream(identifier); err != nil {
			return err
		}
	}

	if err := s.ensureJoinAllowed(channelID, client.user); err != nil {
		return err
	}

	channel := s.GetOrCreateChannel(channelID, namespace)

	// Vérifier les permissions
	if channel.settings.RequireAuth && client.user.ID == "" {
		return ErrAuthRequired
	}

	if len(channel.clients) >= channel.settings.MaxClients {
		return ErrChannelFull
	}

	// Ajouter le client au channel
	channel.mu.Lock()
	channel.clients[client] = true
	channel.mu.Unlock()

	client.mu.Lock()
	client.channels[channelID] = channel
	client.mu.Unlock()

	// Ajouter à la présence
	if channel.settings.AllowPresence && client.user != nil && client.user.ID != "" {
		presence := &PresenceInfo{
			User:     client.user,
			JoinedAt: time.Now(),
			LastSeen: time.Now(),
			ClientID: client.ID,
		}

		channel.mu.Lock()
		channel.presence[client.user.ID] = presence
		channel.mu.Unlock()

		if s.redisManager != nil {
			ctx := context.Background()
			if err := s.redisManager.SavePresence(ctx, channelID, client.user.ID, presence); err != nil {
				s.logger.Error("Failed to persist presence", "error", err, "channel", channelID, "user", client.user.ID)
			}
		}

		// Diffuser événement join
		joinMsg := &Message{
			ID:        generateID(),
			Type:      MessageTypeJoin,
			Channel:   channelID,
			User:      client.user,
			Data:      map[string]interface{}{"presence": presence},
			Timestamp: time.Now(),
		}
		s.broadcast <- joinMsg
	}

	// Envoyer l'historique
	s.sendChannelHistory(client, channel)

	// Confirmer l'abonnement
	confirmMsg := &Message{
		ID:      generateID(),
		Type:    MessageTypeSubscribe,
		Channel: channelID,
		Data: map[string]interface{}{
			"subscribed": true,
			"clients":    len(channel.clients),
		},
		Timestamp: time.Now(),
	}
	client.Send(confirmMsg)

	s.logger.Info("Client subscribed",
		"client_id", client.ID,
		"channel", channelID,
		"clients_in_channel", len(channel.clients))

	return nil
}

func (s *RealtimeServer) unsubscribeFromChannel(client *Client, channelID string, channel *Channel) {
	// Retirer du channel
	channel.mu.Lock()
	delete(channel.clients, client)
	channel.mu.Unlock()

	client.mu.Lock()
	delete(client.channels, channelID)
	client.mu.Unlock()

	// Retirer de la présence et diffuser leave
	if channel.settings.AllowPresence && client.user != nil && client.user.ID != "" {
		channel.mu.Lock()
		delete(channel.presence, client.user.ID)
		channel.mu.Unlock()

		if s.redisManager != nil {
			ctx := context.Background()
			if err := s.redisManager.RemovePresence(ctx, channelID, client.user.ID); err != nil {
				s.logger.Error("Failed to remove presence", "error", err, "channel", channelID, "user", client.user.ID)
			}
		}

		leaveMsg := &Message{
			ID:        generateID(),
			Type:      MessageTypeLeave,
			Channel:   channelID,
			User:      client.user,
			Timestamp: time.Now(),
		}
		s.broadcast <- leaveMsg
	}
}

// Méthodes publiques pour l'API
func (s *RealtimeServer) UpgradeConnection(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	return s.upgrader.Upgrade(w, r, nil)
}

func (s *RealtimeServer) CreateClient(conn *websocket.Conn, user *UserInfo) *Client {
	return &Client{
		ID:       generateClientID(),
		conn:     conn,
		server:   s,
		channels: make(map[string]*Channel),
		user:     user,
		send:     make(chan []byte, 256),
		lastPing: time.Now(),
	}
}

func (s *RealtimeServer) RegisterClient(client *Client) {
	s.register <- client
}

func (s *RealtimeServer) RegisterRPCHandler(method string, handler RPCHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if method == "" || handler == nil {
		return
	}

	s.rpcHandlers[strings.ToLower(method)] = handler
}

func (s *RealtimeServer) ExecuteRPC(ctx context.Context, method string, user *UserInfo, params map[string]interface{}) (map[string]interface{}, error) {
	s.mu.RLock()
	handler, exists := s.rpcHandlers[strings.ToLower(method)]
	s.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("rpc method not found: %s", method)
	}

	if ctx == nil {
		ctx = context.Background()
	}

	return handler(ctx, user, params)
}

func (s *RealtimeServer) PublishMessage(message *Message) error {
	// Vérifier que le channel existe
	s.mu.RLock()
	_, exists := s.channels[message.Channel]
	s.mu.RUnlock()

	if !exists {
		// Créer le channel automatiquement pour les publications REST
		parts := parseChannelID(message.Channel)
		if len(parts) >= 2 {
			s.GetOrCreateChannel(message.Channel, parts[0])
		} else {
			return ErrInvalidChannelID
		}
	}

	if message.User != nil {
		if err := s.ensurePublishAllowed(message.Channel, message.User); err != nil {
			return err
		}
	}

	s.broadcast <- message
	return nil
}

// Helper functions
func (s *RealtimeServer) serializeMessage(msg *Message) []byte {
	data, _ := json.Marshal(msg)
	return data
}

func (s *RealtimeServer) sendChannelHistory(client *Client, channel *Channel) {
	history := channel.GetHistory()
	for _, msg := range history {
		client.Send(msg)
	}
}

func (s *RealtimeServer) saveMessage(message *Message) {
	// Sauvegarder seulement les vrais messages de chat
	if message.Type != MessageTypeChat || message.User == nil {
		return
	}

	// Extraire stream_key du channel "chat:stream_key"
	parts := parseChannelID(message.Channel)
	if len(parts) < 2 || parts[0] != "chat" {
		return
	}

	streamKey := parts[1]
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Récupérer l'ID du stream
		var streamID string
		err := s.db.QueryRow(ctx, "SELECT id FROM streams WHERE key = $1", streamKey).Scan(&streamID)
		if err != nil {
			s.logger.Error("Failed to find stream for chat message", "stream_key", streamKey, "error", err)
			return
		}

		// Sauvegarder le message
		content, _ := json.Marshal(message.Data)
		_, err = s.db.Exec(ctx, `
			INSERT INTO chat_messages (stream_id, user_id, content, username, display_name, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, streamID, message.User.ID, string(content), message.User.Username, message.User.DisplayName, message.Timestamp)

		if err != nil {
			s.logger.Error("Failed to save chat message", "error", err)
		}
	}

	if s.redisManager != nil {
		if err := s.redisManager.SaveMessage(context.Background(), message.Channel, message); err != nil {
			s.logger.Error("Failed to cache chat message", "error", err, "channel", message.Channel)
		}
	}
}

func (s *RealtimeServer) forwardToProxy(endpoint string, message *Message) {
	if endpoint == "" {
		return
	}

	body, err := json.Marshal(message)
	if err != nil {
		s.logger.Error("Failed to marshal proxy message", "error", err)
		return
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		s.logger.Error("Failed to create proxy request", "error", err, "endpoint", endpoint)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.Background())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.logger.Error("Failed to send proxy request", "error", err, "endpoint", endpoint)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		s.logger.Warn("Proxy endpoint returned error", "status", resp.StatusCode, "endpoint", endpoint)
	}
}

// Utility functions
func generateID() string {
	return fmt.Sprintf("%d_%s", time.Now().UnixNano(), randomString(8))
}

func generateClientID() string {
	return fmt.Sprintf("client_%d_%s", time.Now().UnixNano(), randomString(6))
}

func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[time.Now().UnixNano()%int64(len(charset))]
	}
	return string(b)
}

func parseChannelID(channelID string) []string {
	// Ex: "chat:stream_123" -> ["chat", "stream_123"]
	return strings.SplitN(channelID, ":", 2)
}

func (s *RealtimeServer) ensureChatStream(streamKey string) error {
	if s.db == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var chatEnabled bool
	err := s.db.QueryRow(ctx, `SELECT chat_enabled FROM streams WHERE key = $1`, streamKey).Scan(&chatEnabled)
	if err != nil {
		s.logger.Warn("Stream not found for chat", "stream_key", streamKey, "error", err)
		return ErrStreamNotFound
	}

	if !chatEnabled {
		return ErrChatDisabled
	}

	return nil
}

func (s *RealtimeServer) ensureJoinAllowed(channelID string, user *UserInfo) error {
	parts := parseChannelID(channelID)
	if len(parts) < 2 {
		return ErrInvalidChannelID
	}

	private, allowedRoles, allowedUsers := s.resolveAccessRules(channelID, parts[0])

	if private {
		if user == nil || user.ID == "" {
			return ErrUserNotAuthorized
		}

		if len(allowedRoles) > 0 && !hasRole(user.Role, allowedRoles) {
			return ErrUserNotAuthorized
		}

		if len(allowedUsers) > 0 && !containsID(user.ID, allowedUsers) {
			return ErrUserNotAuthorized
		}

		if s.redisManager != nil {
			authorized, err := s.redisManager.IsUserAuthorized(context.Background(), channelID, user.ID)
			if err != nil {
				return err
			}
			if !authorized {
				return ErrUserNotAuthorized
			}
		} else {
			s.mu.RLock()
			if users, ok := s.channelAccess[channelID]; ok && len(users) > 0 {
				if !users[user.ID] {
					s.mu.RUnlock()
					return ErrUserNotAuthorized
				}
			}
			s.mu.RUnlock()
		}
	}

	return s.checkBan(channelID, user)
}

func (s *RealtimeServer) ensurePublishAllowed(channelID string, user *UserInfo) error {
	if err := s.ensureJoinAllowed(channelID, user); err != nil {
		return err
	}

	return s.checkTimeout(channelID, user)
}

// EnsurePublishAllowed expose la vérification publique pour les handlers HTTP
func (s *RealtimeServer) EnsurePublishAllowed(channelID string, user *UserInfo) error {
	return s.ensurePublishAllowed(channelID, user)
}

// VerifyChatStream permet aux handlers HTTP de valider l'existence d'un stream pour le namespace chat
func (s *RealtimeServer) VerifyChatStream(streamKey string) error {
	return s.ensureChatStream(streamKey)
}

func (s *RealtimeServer) SaveTimeout(ctx context.Context, channelID, userID, reason, moderatorID string, duration time.Duration) error {
	if duration <= 0 {
		duration = 5 * time.Minute
	}

	if s.redisManager == nil {
		s.storeTimeout(channelID, userID, &TimeoutInfo{
			Reason:    reason,
			Duration:  duration,
			TimeoutBy: moderatorID,
			TimeoutAt: time.Now(),
			ExpiresAt: time.Now().Add(duration),
		})
		return nil
	}

	timeout := &TimeoutInfo{
		Reason:    reason,
		Duration:  duration,
		TimeoutBy: moderatorID,
		TimeoutAt: time.Now(),
		ExpiresAt: time.Now().Add(duration),
	}

	return s.redisManager.SaveTimeout(ctx, channelID, userID, timeout, duration)
}

func (s *RealtimeServer) SaveBan(ctx context.Context, channelID, userID, reason, moderatorID string) error {
	if s.redisManager == nil {
		s.storeBan(channelID, userID, &BanInfo{
			Reason:   reason,
			BannedBy: moderatorID,
			BannedAt: time.Now(),
		})
		return nil
	}

	ban := &BanInfo{
		Reason:   reason,
		BannedBy: moderatorID,
		BannedAt: time.Now(),
	}

	return s.redisManager.SaveBan(ctx, channelID, userID, ban)
}

func (s *RealtimeServer) RemoveBan(ctx context.Context, channelID, userID string) error {
	if s.redisManager == nil {
		s.deleteBan(channelID, userID)
		return nil
	}

	return s.redisManager.RemoveBan(ctx, channelID, userID)
}

func (s *RealtimeServer) UpdateChannelSettings(channelID string, updates *ChannelSettings) (*ChannelSettings, error) {
	parts := parseChannelID(channelID)
	if len(parts) < 2 {
		return nil, ErrInvalidChannelID
	}

	channel := s.GetOrCreateChannel(channelID, parts[0])
	channel.UpdateSettings(updates)

	return channel.settings, nil
}

func (s *RealtimeServer) GrantChannelAccess(ctx context.Context, channelID string, userIDs []string) error {
	if s.redisManager == nil {
		s.grantAccess(channelID, userIDs)
		return nil
	}

	if ctx == nil {
		ctx = context.Background()
	}

	return s.redisManager.GrantChannelAccess(ctx, channelID, userIDs)
}

func (s *RealtimeServer) RevokeChannelAccess(ctx context.Context, channelID, userID string) error {
	if s.redisManager == nil {
		s.revokeAccess(channelID, userID)
		return nil
	}

	if ctx == nil {
		ctx = context.Background()
	}

	return s.redisManager.RevokeChannelAccess(ctx, channelID, userID)
}

func (s *RealtimeServer) SyncPresence(ctx context.Context, channelID string, entries []*PresenceInfo) error {
	parts := parseChannelID(channelID)
	if len(parts) < 2 {
		return ErrInvalidChannelID
	}

	if ctx == nil {
		ctx = context.Background()
	}

	channel := s.GetOrCreateChannel(channelID, parts[0])

	channel.mu.Lock()
	channel.presence = make(map[string]*PresenceInfo)
	for _, entry := range entries {
		if entry == nil || entry.User == nil || entry.User.ID == "" {
			continue
		}
		channel.presence[entry.User.ID] = entry
	}
	channel.mu.Unlock()

	if s.redisManager != nil {
		if err := s.redisManager.SetPresenceSnapshot(ctx, channelID, channel.presence); err != nil {
			return err
		}
	}

	return nil
}

func (s *RealtimeServer) resolveAccessRules(channelID, namespace string) (bool, []string, []string) {
	private := false
	var roles []string
	var users []string

	s.mu.RLock()
	channel := s.channels[channelID]
	s.mu.RUnlock()

	if channel != nil && channel.settings != nil {
		private = channel.settings.Private
		if len(channel.settings.AllowedRoles) > 0 {
			roles = append([]string(nil), channel.settings.AllowedRoles...)
		}
	}

	if s.config != nil {
		if nsCfg := s.config.namespaceConfig(namespace); nsCfg != nil {
			if nsCfg.Private != nil && channel == nil {
				private = *nsCfg.Private
			}
			if len(nsCfg.AllowedRoles) > 0 && len(roles) == 0 {
				roles = append([]string(nil), nsCfg.AllowedRoles...)
			}
		}
		if chCfg := s.config.channelConfig(channelID, namespace); chCfg != nil {
			if chCfg.Private != nil {
				private = *chCfg.Private
			}
			if len(chCfg.AllowedRoles) > 0 {
				roles = append([]string(nil), chCfg.AllowedRoles...)
			}
			if len(chCfg.AllowedUsers) > 0 {
				users = append([]string(nil), chCfg.AllowedUsers...)
			}
		}
	}

	return private, roles, users
}

func hasRole(userRole string, allowed []string) bool {
	for _, role := range allowed {
		if strings.EqualFold(role, userRole) {
			return true
		}
	}
	return false
}

func containsID(id string, list []string) bool {
	for _, v := range list {
		if v == id {
			return true
		}
	}
	return false
}

func (s *RealtimeServer) checkBan(channelID string, user *UserInfo) error {
	if user == nil || user.ID == "" {
		return nil
	}

	var (
		banned  bool
		banInfo *BanInfo
		err     error
	)

	if s.redisManager != nil {
		banned, banInfo, err = s.redisManager.IsBanned(context.Background(), channelID, user.ID)
		if err != nil {
			return err
		}
	} else {
		banInfo, banned = s.getBan(channelID, user.ID)
	}

	if banned {
		if banInfo != nil && banInfo.Reason != "" {
			return fmt.Errorf("%w: %s", ErrUserBanned, banInfo.Reason)
		}
		return ErrUserBanned
	}

	return nil
}

func (s *RealtimeServer) checkTimeout(channelID string, user *UserInfo) error {
	if user == nil || user.ID == "" {
		return nil
	}

	var (
		timedOut    bool
		timeoutInfo *TimeoutInfo
		err         error
	)

	if s.redisManager != nil {
		timedOut, timeoutInfo, err = s.redisManager.IsTimedOut(context.Background(), channelID, user.ID)
		if err != nil {
			return err
		}
	} else {
		timeoutInfo, timedOut = s.getTimeout(channelID, user.ID)
	}

	if timedOut {
		if timeoutInfo != nil && timeoutInfo.ExpiresAt.Before(time.Now()) {
			s.clearTimeout(channelID, user.ID)
			return nil
		}
		if timeoutInfo != nil && timeoutInfo.Reason != "" {
			return fmt.Errorf("%w: %s", ErrUserTimedOut, timeoutInfo.Reason)
		}
		return ErrUserTimedOut
	}

	return nil
}

// Errors
var (
	ErrInvalidChannelID  = fmt.Errorf("invalid channel ID")
	ErrAuthRequired      = fmt.Errorf("authentication required")
	ErrChannelFull       = fmt.Errorf("channel is full")
	ErrStreamNotFound    = fmt.Errorf("stream not found")
	ErrChatDisabled      = fmt.Errorf("chat disabled for stream")
	ErrUserBanned        = fmt.Errorf("user banned from channel")
	ErrUserTimedOut      = fmt.Errorf("user timed out")
	ErrUserNotAuthorized = fmt.Errorf("user not authorized for channel")
)

func (s *RealtimeServer) storeBan(channelID, userID string, ban *BanInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.bans[channelID]; !exists {
		s.bans[channelID] = make(map[string]*BanInfo)
	}
	s.bans[channelID][userID] = ban
}

func (s *RealtimeServer) deleteBan(channelID, userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if users, exists := s.bans[channelID]; exists {
		delete(users, userID)
		if len(users) == 0 {
			delete(s.bans, channelID)
		}
	}
}

func (s *RealtimeServer) getBan(channelID, userID string) (*BanInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if users, exists := s.bans[channelID]; exists {
		ban, ok := users[userID]
		return ban, ok
	}
	return nil, false
}

func (s *RealtimeServer) storeTimeout(channelID, userID string, timeout *TimeoutInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.timeouts[channelID]; !exists {
		s.timeouts[channelID] = make(map[string]*TimeoutInfo)
	}
	s.timeouts[channelID][userID] = timeout
}

func (s *RealtimeServer) getTimeout(channelID, userID string) (*TimeoutInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	users, exists := s.timeouts[channelID]
	if !exists {
		return nil, false
	}

	timeout, ok := users[userID]
	if !ok {
		return nil, false
	}

	if timeout.ExpiresAt.Before(time.Now()) {
		delete(users, userID)
		if len(users) == 0 {
			delete(s.timeouts, channelID)
		}
		return nil, false
	}

	return timeout, true
}

func (s *RealtimeServer) clearTimeout(channelID, userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if users, exists := s.timeouts[channelID]; exists {
		delete(users, userID)
		if len(users) == 0 {
			delete(s.timeouts, channelID)
		}
	}
}

func (s *RealtimeServer) grantAccess(channelID string, userIDs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.channelAccess[channelID]; !exists {
		s.channelAccess[channelID] = make(map[string]bool)
	}
	for _, userID := range userIDs {
		if userID == "" {
			continue
		}
		s.channelAccess[channelID][userID] = true
	}
}

func (s *RealtimeServer) revokeAccess(channelID, userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if users, exists := s.channelAccess[channelID]; exists {
		delete(users, userID)
		if len(users) == 0 {
			delete(s.channelAccess, channelID)
		}
	}
}
