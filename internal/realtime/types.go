// api/internal/realtime/types.go
package realtime

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Message structure pour tous les messages
type Message struct {
	ID        string                 `json:"id"`
	Type      MessageType            `json:"type"`
	Channel   string                 `json:"channel"`
	Data      map[string]interface{} `json:"data"`
	User      *UserInfo              `json:"user,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
}

type MessageType string

const (
	MessageTypeConnect     MessageType = "connect"
	MessageTypeDisconnect  MessageType = "disconnect"
	MessageTypeSubscribe   MessageType = "subscribe"
	MessageTypeUnsubscribe MessageType = "unsubscribe"
	MessageTypePublish     MessageType = "publish"
	MessageTypePresence    MessageType = "presence"
	MessageTypeJoin        MessageType = "join"
	MessageTypeLeave       MessageType = "leave"
	MessageTypeChat        MessageType = "chat"
	MessageTypeSystem      MessageType = "system"
	MessageTypeError       MessageType = "error"
	MessageTypePing        MessageType = "ping"
	MessageTypePong        MessageType = "pong"
	MessageTypeRPC         MessageType = "rpc"
)

type UserInfo struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Avatar      string `json:"avatar,omitempty"`
	Role        string `json:"role,omitempty"`
}

type PresenceInfo struct {
	User     *UserInfo `json:"user"`
	JoinedAt time.Time `json:"joined_at"`
	LastSeen time.Time `json:"last_seen"`
	ClientID string    `json:"client_id"`
}

type ChannelSettings struct {
	MaxClients    int           `json:"max_clients"`
	MaxHistory    int           `json:"max_history"`
	TTL           time.Duration `json:"ttl" swaggertype:"integer" format:"int64" example:"3600000000000"`
	RequireAuth   bool          `json:"require_auth"`
	AllowPublish  bool          `json:"allow_publish"`
	AllowPresence bool          `json:"allow_presence"`
	Private       bool          `json:"private"`
	AllowedRoles  []string      `json:"allowed_roles,omitempty"`
	ProxyEndpoint string        `json:"proxy_endpoint,omitempty"`
}

// Channel représente un canal (ex: chat:stream_123)
type Channel struct {
	ID         string
	Name       string
	Namespace  string
	clients    map[*Client]bool
	history    []*Message
	maxHistory int
	presence   map[string]*PresenceInfo
	settings   *ChannelSettings
	mu         sync.RWMutex
}

func (c *Channel) UpdateSettings(updates *ChannelSettings) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if updates == nil {
		return
	}

	if updates.MaxClients != 0 {
		c.settings.MaxClients = updates.MaxClients
	}
	if updates.MaxHistory != 0 && updates.MaxHistory != c.settings.MaxHistory {
		c.settings.MaxHistory = updates.MaxHistory
		c.maxHistory = updates.MaxHistory
	}
	if updates.TTL != 0 {
		c.settings.TTL = updates.TTL
	}
	if updates.AllowedRoles != nil {
		c.settings.AllowedRoles = updates.AllowedRoles
	}
	if updates.ProxyEndpoint != "" || c.settings.ProxyEndpoint != "" {
		c.settings.ProxyEndpoint = updates.ProxyEndpoint
	}
	c.settings.RequireAuth = updates.RequireAuth
	c.settings.AllowPublish = updates.AllowPublish
	c.settings.AllowPresence = updates.AllowPresence
	c.settings.Private = updates.Private
}

// Client représente une connexion WebSocket
type Client struct {
	ID       string
	conn     *websocket.Conn
	server   *RealtimeServer
	channels map[string]*Channel
	user     *UserInfo
	send     chan []byte
	lastPing time.Time
	mu       sync.RWMutex
}

// NewChannel crée un nouveau channel
func NewChannel(id, namespace string, settings *ChannelSettings) *Channel {
	return &Channel{
		ID:         id,
		Name:       id,
		Namespace:  namespace,
		clients:    make(map[*Client]bool),
		history:    make([]*Message, 0),
		maxHistory: settings.MaxHistory,
		presence:   make(map[string]*PresenceInfo),
		settings:   settings,
	}
}

func (c *Channel) GetSettings() *ChannelSettings {
	c.mu.RLock()
	defer c.mu.RUnlock()

	copySettings := *c.settings
	if c.settings.AllowedRoles != nil {
		copySettings.AllowedRoles = append([]string(nil), c.settings.AllowedRoles...)
	}
	return &copySettings
}

// AddToHistory ajoute un message à l'historique du channel
func (c *Channel) AddToHistory(message *Message) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.history = append(c.history, message)

	// Limiter la taille de l'historique
	if len(c.history) > c.maxHistory {
		c.history = c.history[1:]
	}
}

// GetHistory retourne l'historique du channel
func (c *Channel) GetHistory() []*Message {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Copie pour éviter les race conditions
	history := make([]*Message, len(c.history))
	copy(history, c.history)
	return history
}

// GetPresence retourne la liste des utilisateurs présents
func (c *Channel) GetPresence() map[string]*PresenceInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Copie de la présence
	presence := make(map[string]*PresenceInfo)
	for k, v := range c.presence {
		presence[k] = v
	}
	return presence
}

// GetClientCount retourne le nombre de clients connectés
func (c *Channel) GetClientCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.clients)
}

// Send envoie un message au client
func (c *Client) Send(message *Message) {
	data := c.server.serializeMessage(message)
	select {
	case c.send <- data:
	default:
		close(c.send)
	}
}

// ReadPump gère la lecture des messages WebSocket
func (c *Client) ReadPump() {
	defer func() {
		c.server.unregister <- c
		c.conn.Close()
	}()

	// Configuration des timeouts
	c.conn.SetReadLimit(512)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		c.lastPing = time.Now()
		return nil
	})

	for {
		_, messageData, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.server.logger.Error("WebSocket error", "client_id", c.ID, "error", err)
			}
			break
		}

		c.handleMessage(messageData)
	}
}

// WritePump gère l'écriture des messages WebSocket
func (c *Client) WritePump() {
	ticker := time.NewTicker(54 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// handleMessage traite les messages reçus du client
func (c *Client) handleMessage(data []byte) {
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		c.sendError("Invalid message format")
		return
	}

	switch msg.Type {
	case MessageTypeSubscribe:
		channelID := msg.Channel
		if channelID == "" {
			c.sendError("Channel ID required for subscription")
			return
		}
		if err := c.server.subscribeToChannel(c, channelID); err != nil {
			c.sendError("Subscription failed: " + err.Error())
		}

	case MessageTypeUnsubscribe:
		channelID := msg.Channel
		if channel, exists := c.channels[channelID]; exists {
			c.server.unsubscribeFromChannel(c, channelID, channel)

			// Confirmer la désinscription
			confirmMsg := &Message{
				ID:        generateID(),
				Type:      MessageTypeUnsubscribe,
				Channel:   channelID,
				Data:      map[string]interface{}{"unsubscribed": true},
				Timestamp: time.Now(),
			}
			c.Send(confirmMsg)
		}

	case MessageTypePublish:
		c.handlePublish(&msg)

	case MessageTypePresence:
		c.handlePresenceRequest(&msg)

	case MessageTypePing:
		// Répondre avec un pong
		pongMsg := &Message{
			ID:        generateID(),
			Type:      MessageTypePong,
			Timestamp: time.Now(),
		}
		c.Send(pongMsg)

	case MessageTypeRPC:
		c.handleRPC(&msg)
	}
}

// handlePublish traite les messages de publication
func (c *Client) handlePublish(msg *Message) {
	channel, exists := c.channels[msg.Channel]
	if !exists {
		c.sendError("Not subscribed to channel")
		return
	}

	if !channel.settings.AllowPublish {
		c.sendError("Publishing not allowed on this channel")
		return
	}

	if err := c.server.ensurePublishAllowed(msg.Channel, c.user); err != nil {
		c.sendError(err.Error())
		return
	}

	// Créer le message à diffuser
	broadcastMsg := &Message{
		ID:        generateID(),
		Type:      MessageTypeChat,
		Channel:   msg.Channel,
		Data:      msg.Data,
		User:      c.user,
		Timestamp: time.Now(),
	}

	// Diffuser
	c.server.broadcast <- broadcastMsg
}

// handlePresenceRequest traite les demandes de présence
func (c *Client) handlePresenceRequest(msg *Message) {
	channel, exists := c.channels[msg.Channel]
	if !exists {
		c.sendError("Not subscribed to channel")
		return
	}

	if !channel.settings.AllowPresence {
		c.sendError("Presence not allowed on this channel")
		return
	}

	// Envoyer la liste de présence
	presence := channel.GetPresence()
	presenceMsg := &Message{
		ID:      generateID(),
		Type:    MessageTypePresence,
		Channel: msg.Channel,
		Data: map[string]interface{}{
			"presence": presence,
			"count":    len(presence),
		},
		Timestamp: time.Now(),
	}
	c.Send(presenceMsg)
}

func (c *Client) handleRPC(msg *Message) {
	methodRaw, ok := msg.Data["method"]
	if !ok {
		c.sendError("RPC method required")
		return
	}
	method, ok := methodRaw.(string)
	if !ok || method == "" {
		c.sendError("Invalid RPC method")
		return
	}

	params := map[string]interface{}{}
	if raw, exists := msg.Data["params"]; exists {
		if decoded, ok := raw.(map[string]interface{}); ok {
			params = decoded
		}
	}

	result, rpcErr := c.server.ExecuteRPC(context.Background(), method, c.user, params)
	responseData := map[string]interface{}{
		"method": method,
	}
	if rpcErr != nil {
		responseData["error"] = rpcErr.Error()
	} else {
		responseData["result"] = result
	}

	resp := &Message{
		ID:        generateID(),
		Type:      MessageTypeRPC,
		Data:      responseData,
		Timestamp: time.Now(),
	}
	c.Send(resp)
}

// sendError envoie un message d'erreur au client
func (c *Client) sendError(errMsg string) {
	errorMessage := &Message{
		ID:        generateID(),
		Type:      MessageTypeError,
		Data:      map[string]interface{}{"error": errMsg},
		Timestamp: time.Now(),
	}
	c.Send(errorMessage)
}
