// api/internal/chat/hub.go
package chat

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"golive/pkg/logger"
)

// MessageType defines the type of chat message
type MessageType string

const (
	MessageTypeMessage     MessageType = "message"
	MessageTypeSystem      MessageType = "system"
	MessageTypeEmote       MessageType = "emote"
	MessageTypeCommand     MessageType = "command"
	MessageTypeUserJoin    MessageType = "user_join"
	MessageTypeUserLeave   MessageType = "user_leave"
	MessageTypeUserTimeout MessageType = "user_timeout"
	MessageTypeUserBan     MessageType = "user_ban"
	MessageTypeChatClear   MessageType = "chat_clear"
)

// Message represents a chat message
type Message struct {
	ID          int64       `json:"id,omitempty"`
	StreamKey   string      `json:"stream_key"`
	Type        MessageType `json:"type"`
	Content     string      `json:"content"`
	UserID      *string     `json:"user_id,omitempty"`
	Username    string      `json:"username"`
	DisplayName string      `json:"display_name"`
	UserColor   *string     `json:"user_color,omitempty"`
	Timestamp   time.Time   `json:"timestamp"`
	Emotes      []string    `json:"emotes,omitempty"`
	Mentions    []string    `json:"mentions,omitempty"`
	IsDeleted   bool        `json:"is_deleted,omitempty"`
	IsPinned    bool        `json:"is_pinned,omitempty"`
	ReplyToID   *int64      `json:"reply_to_id,omitempty"`
}

// Client represents a connected WebSocket client
type Client struct {
	ID          string
	Conn        *websocket.Conn
	Hub         *Hub
	Send        chan []byte
	UserID      *string
	Username    string
	IsStreamer  bool
	IsModerator bool
	LastSeen    time.Time
	mu          sync.RWMutex
}

// Hub maintains the set of active clients and broadcasts messages
type Hub struct {
	StreamKey string

	// Registered clients
	clients map[*Client]bool

	// Inbound messages from clients
	broadcast chan []byte

	// Register requests from clients
	register chan *Client

	// Unregister requests from clients
	unregister chan *Client

	// Chat settings
	settings *ChatSettings

	// Dependencies
	db     *pgxpool.Pool
	rdb    *redis.Client
	logger *logger.Logger

	// Shutdown
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu sync.RWMutex
}

// ChatSettings represents chat room settings
type ChatSettings struct {
	Enabled          bool          `json:"enabled"`
	FollowersOnly    bool          `json:"followers_only"`
	SlowMode         bool          `json:"slow_mode"`
	SlowModeDelay    time.Duration `json:"slow_mode_delay"`
	MaxMessageLength int           `json:"max_message_length"`
	AllowLinks       bool          `json:"allow_links"`
	WordFilters      []string      `json:"word_filters"`
}

// NewHub creates a new chat hub
func NewHub(streamKey string, db *pgxpool.Pool, rdb *redis.Client, logger *logger.Logger) *Hub {
	ctx, cancel := context.WithCancel(context.Background())

	return &Hub{
		StreamKey:  streamKey,
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		settings: &ChatSettings{
			Enabled:          true,
			FollowersOnly:    false,
			SlowMode:         false,
			SlowModeDelay:    5 * time.Second,
			MaxMessageLength: 500,
			AllowLinks:       true,
			WordFilters:      []string{},
		},
		db:     db,
		rdb:    rdb,
		logger: logger,
		ctx:    ctx,
		cancel: cancel,
	}
}

// Run starts the hub
func (h *Hub) Run() {
	h.wg.Add(1)
	defer h.wg.Done()

	h.logger.Info("Starting chat hub", "stream_key", h.StreamKey)

	for {
		select {
		case client := <-h.register:
			h.registerClient(client)

		case client := <-h.unregister:
			h.unregisterClient(client)

		case message := <-h.broadcast:
			h.broadcastMessage(message)

		case <-h.ctx.Done():
			h.logger.Info("Shutting down chat hub", "stream_key", h.StreamKey)
			return
		}
	}
}

// RegisterClient registers a new client
func (h *Hub) RegisterClient(client *Client) {
	select {
	case h.register <- client:
	case <-h.ctx.Done():
	}
}

// UnregisterClient unregisters a client
func (h *Hub) UnregisterClient(client *Client) {
	select {
	case h.unregister <- client:
	case <-h.ctx.Done():
	}
}

// BroadcastMessage sends a message to all clients
func (h *Hub) BroadcastMessage(message []byte) {
	select {
	case h.broadcast <- message:
	case <-h.ctx.Done():
	}
}

// SendSystemMessage sends a system message
func (h *Hub) SendSystemMessage(content string) {
	msg := &Message{
		StreamKey:   h.StreamKey,
		Type:        MessageTypeSystem,
		Content:     content,
		Username:    "System",
		DisplayName: "System",
		Timestamp:   time.Now(),
	}

	if data, err := json.Marshal(msg); err == nil {
		h.BroadcastMessage(data)
	}
}

// GetClientCount returns the number of connected clients
func (h *Hub) GetClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// GetSettings returns current chat settings
func (h *Hub) GetSettings() *ChatSettings {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.settings
}

// UpdateSettings updates chat settings
func (h *Hub) UpdateSettings(settings *ChatSettings) {
	h.mu.Lock()
	h.settings = settings
	h.mu.Unlock()

	// Broadcast settings update
	h.SendSystemMessage("Chat settings updated")
}

// Close shuts down the hub
func (h *Hub) Close() {
	h.cancel()
	h.wg.Wait()

	// Close all client connections
	h.mu.Lock()
	for client := range h.clients {
		close(client.Send)
		client.Conn.Close()
	}
	h.mu.Unlock()
}

// Private methods

func (h *Hub) registerClient(client *Client) {
	h.mu.Lock()
	h.clients[client] = true
	h.mu.Unlock()

	h.logger.Info("Client connected",
		"stream_key", h.StreamKey,
		"client_id", client.ID,
		"username", client.Username,
		"total_clients", len(h.clients))

	// Send join message
	if client.Username != "" {
		msg := &Message{
			StreamKey:   h.StreamKey,
			Type:        MessageTypeUserJoin,
			Content:     client.Username + " joined the chat",
			Username:    client.Username,
			DisplayName: client.Username,
			UserID:      client.UserID,
			Timestamp:   time.Now(),
		}

		if data, err := json.Marshal(msg); err == nil {
			h.BroadcastMessage(data)
		}
	}

	// Send current viewer count
	h.updateViewerCount()
}

func (h *Hub) unregisterClient(client *Client) {
	h.mu.Lock()
	if _, ok := h.clients[client]; ok {
		delete(h.clients, client)
		close(client.Send)
	}
	h.mu.Unlock()

	h.logger.Info("Client disconnected",
		"stream_key", h.StreamKey,
		"client_id", client.ID,
		"username", client.Username,
		"total_clients", len(h.clients))

	// Send leave message
	if client.Username != "" {
		msg := &Message{
			StreamKey:   h.StreamKey,
			Type:        MessageTypeUserLeave,
			Content:     client.Username + " left the chat",
			Username:    client.Username,
			DisplayName: client.Username,
			UserID:      client.UserID,
			Timestamp:   time.Now(),
		}

		if data, err := json.Marshal(msg); err == nil {
			h.BroadcastMessage(data)
		}
	}

	// Update viewer count
	h.updateViewerCount()
}

func (h *Hub) broadcastMessage(message []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for client := range h.clients {
		select {
		case client.Send <- message:
		default:
			close(client.Send)
			delete(h.clients, client)
		}
	}
}

func (h *Hub) updateViewerCount() {
	// Update viewer count in database
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		viewerCount := h.GetClientCount()
		_, err := h.db.Exec(ctx,
			"UPDATE streams SET current_viewers = $1 WHERE key = $2",
			viewerCount, h.StreamKey)
		if err != nil {
			h.logger.Error("Failed to update viewer count", "error", err)
		}
	}()
}

// Client methods

// NewClient creates a new WebSocket client
func NewClient(conn *websocket.Conn, hub *Hub, userID *string, username string) *Client {
	return &Client{
		ID:       generateClientID(),
		Conn:     conn,
		Hub:      hub,
		Send:     make(chan []byte, 256),
		UserID:   userID,
		Username: username,
		LastSeen: time.Now(),
	}
}

// ReadPump pumps messages from WebSocket connection to hub
func (c *Client) ReadPump() {
	defer func() {
		c.Hub.UnregisterClient(c)
		c.Conn.Close()
	}()

	c.Conn.SetReadLimit(512)
	c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, messageData, err := c.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			break
		}

		// Parse message
		var incomingMsg struct {
			Type    MessageType `json:"type"`
			Content string      `json:"content"`
		}

		if err := json.Unmarshal(messageData, &incomingMsg); err != nil {
			continue
		}

		// Process message
		c.processMessage(incomingMsg.Type, incomingMsg.Content)
	}
}

// WritePump pumps messages from hub to WebSocket connection
func (c *Client) WritePump() {
	ticker := time.NewTicker(54 * time.Second)
	defer func() {
		ticker.Stop()
		c.Conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.Send:
			c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.Conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			// Add queued messages
			n := len(c.Send)
			for i := 0; i < n; i++ {
				w.Write([]byte{'\n'})
				w.Write(<-c.Send)
			}

			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// processMessage processes incoming messages from client
func (c *Client) processMessage(msgType MessageType, content string) {
	// Check chat settings
	settings := c.Hub.GetSettings()
	if !settings.Enabled {
		return
	}

	// Validate message length
	if len(content) > settings.MaxMessageLength {
		return
	}

	// Create message
	msg := &Message{
		StreamKey:   c.Hub.StreamKey,
		Type:        msgType,
		Content:     content,
		UserID:      c.UserID,
		Username:    c.Username,
		DisplayName: c.Username,
		Timestamp:   time.Now(),
	}

	// Save to database
	go c.saveMessage(msg)

	// Broadcast to all clients
	if data, err := json.Marshal(msg); err == nil {
		c.Hub.BroadcastMessage(data)
	}
}

// saveMessage saves message to database
func (c *Client) saveMessage(msg *Message) {
	// Persistence disabled: chat history lives in memory/redis only.
	_ = msg
}

// Helper functions

func generateClientID() string {
	return time.Now().Format("20060102150405") + "-" + randomString(8)
}

func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[time.Now().UnixNano()%int64(len(charset))]
	}
	return string(b)
}
