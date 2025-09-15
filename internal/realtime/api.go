// api/internal/realtime/api.go
package realtime

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Méthodes supplémentaires pour l'API REST

// GetChannelHistory retourne l'historique d'un channel avec pagination
func (s *RealtimeServer) GetChannelHistory(channelID string, limit int, since string) ([]*Message, error) {
	if s.redisManager != nil {
		if cached, err := s.redisManager.GetMessages(context.Background(), channelID, limit, since); err == nil && len(cached) > 0 {
			return cached, nil
		} else if err != nil {
			s.logger.Error("Failed to load history from Redis", "channel", channelID, "error", err)
		}
	}

	s.mu.RLock()
	channel := s.channels[channelID]
	s.mu.RUnlock()

	if channel == nil {
		return nil, fmt.Errorf("channel not found")
	}

	history := channel.GetHistory()

	// Filtrer depuis un message ID si spécifié
	if since != "" {
		filtered := make([]*Message, 0)
		found := false
		for _, msg := range history {
			if found {
				filtered = append(filtered, msg)
			} else if msg.ID == since {
				found = true
			}
		}
		history = filtered
	}

	// Limiter le nombre de messages
	if len(history) > limit {
		history = history[len(history)-limit:]
	}

	return history, nil
}

// GetChannelPresence retourne la présence d'un channel
func (s *RealtimeServer) GetChannelPresence(channelID string) (map[string]*PresenceInfo, error) {
	if s.redisManager != nil {
		if presence, err := s.redisManager.GetPresence(context.Background(), channelID); err == nil && len(presence) > 0 {
			return presence, nil
		} else if err != nil {
			s.logger.Error("Failed to load presence from Redis", "channel", channelID, "error", err)
		}
	}

	s.mu.RLock()
	channel := s.channels[channelID]
	s.mu.RUnlock()

	if channel == nil {
		return nil, fmt.Errorf("channel not found")
	}

	return channel.GetPresence(), nil
}

// GetChannelInfo retourne les informations d'un channel
func (s *RealtimeServer) GetChannelInfo(channelID string) (*ChannelInfo, error) {
	s.mu.RLock()
	channel := s.channels[channelID]
	s.mu.RUnlock()

	if channel == nil {
		return nil, fmt.Errorf("channel not found")
	}

	return &ChannelInfo{
		ID:          channel.ID,
		Namespace:   channel.Namespace,
		ClientCount: channel.GetClientCount(),
		Settings:    channel.settings,
		CreatedAt:   time.Now(), // TODO: track actual creation time
	}, nil
}

// DeleteMessage supprime un message de l'historique
func (s *RealtimeServer) DeleteMessage(channelID, messageID string) error {
	s.mu.RLock()
	channel := s.channels[channelID]
	s.mu.RUnlock()

	if channel == nil {
		return fmt.Errorf("channel not found")
	}

	// Marquer le message comme supprimé dans l'historique
	channel.mu.Lock()
	for i, msg := range channel.history {
		if msg.ID == messageID {
			// Remplacer par un message de suppression
			channel.history[i] = &Message{
				ID:        messageID,
				Type:      MessageTypeSystem,
				Channel:   channelID,
				Data:      map[string]interface{}{"deleted": true, "original_timestamp": msg.Timestamp},
				Timestamp: time.Now(),
			}
			break
		}
	}
	channel.mu.Unlock()

	if s.redisManager != nil {
		if err := s.redisManager.DeleteMessage(context.Background(), channelID, messageID); err != nil {
			s.logger.Error("Failed to delete message from Redis", "channel", channelID, "message_id", messageID, "error", err)
		}
	}

	return nil
}

// ClearChannel vide l'historique d'un channel
func (s *RealtimeServer) ClearChannel(channelID string) error {
	s.mu.RLock()
	channel := s.channels[channelID]
	s.mu.RUnlock()

	if channel == nil {
		return fmt.Errorf("channel not found")
	}

	// Vider l'historique
	channel.mu.Lock()
	channel.history = make([]*Message, 0)
	channel.mu.Unlock()

	if s.redisManager != nil {
		if err := s.redisManager.ClearMessages(context.Background(), channelID); err != nil {
			s.logger.Error("Failed to clear channel history from Redis", "channel", channelID, "error", err)
		}
	}

	return nil
}

// BroadcastToChannel diffuse un message à tous les clients d'un channel
func (s *RealtimeServer) BroadcastToChannel(channelID string, message *Message) error {
	message.Channel = channelID
	return s.PublishMessage(message)
}

// GetChannelList retourne la liste de tous les channels
func (s *RealtimeServer) GetChannelList() []*ChannelInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	channels := make([]*ChannelInfo, 0, len(s.channels))
	for _, channel := range s.channels {
		channels = append(channels, &ChannelInfo{
			ID:          channel.ID,
			Namespace:   channel.Namespace,
			ClientCount: channel.GetClientCount(),
			Settings:    channel.settings,
			CreatedAt:   time.Now(), // TODO: track actual creation time
		})
	}

	return channels
}

// GetClientList retourne la liste de tous les clients connectés
func (s *RealtimeServer) GetClientList() []*ClientInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	clients := make([]*ClientInfo, 0, len(s.clients))
	for client := range s.clients {
		channelList := make([]string, 0, len(client.channels))
		for channelID := range client.channels {
			channelList = append(channelList, channelID)
		}

		clients = append(clients, &ClientInfo{
			ID:        client.ID,
			User:      client.user,
			Channels:  channelList,
			LastPing:  client.lastPing,
			Connected: time.Now(), // TODO: track actual connection time
		})
	}

	return clients
}

// DisconnectClient déconnecte un client spécifique
func (s *RealtimeServer) DisconnectClient(clientID string) error {
	s.mu.RLock()
	var targetClient *Client
	for client := range s.clients {
		if client.ID == clientID {
			targetClient = client
			break
		}
	}
	s.mu.RUnlock()

	if targetClient == nil {
		return fmt.Errorf("client not found")
	}

	// Fermer la connexion
	targetClient.conn.Close()
	return nil
}

// GetStats retourne les statistiques du serveur
func (s *RealtimeServer) GetStats() *ServerStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	totalChannels := len(s.channels)
	totalClients := len(s.clients)

	// Calculer les statistiques par namespace
	namespaceStats := make(map[string]*NamespaceStats)
	for _, channel := range s.channels {
		if _, exists := namespaceStats[channel.Namespace]; !exists {
			namespaceStats[channel.Namespace] = &NamespaceStats{
				Channels: 0,
				Clients:  0,
			}
		}
		namespaceStats[channel.Namespace].Channels++
		namespaceStats[channel.Namespace].Clients += channel.GetClientCount()
	}

	return &ServerStats{
		TotalChannels:  totalChannels,
		TotalClients:   totalClients,
		NamespaceStats: namespaceStats,
		Uptime:         time.Since(time.Now()), // TODO: track actual uptime
		LastUpdated:    time.Now(),
	}
}

// CloseChannel ferme un channel et déconnecte tous les clients
func (s *RealtimeServer) CloseChannel(channelID string) error {
	s.mu.Lock()
	channel, exists := s.channels[channelID]
	if !exists {
		s.mu.Unlock()
		return fmt.Errorf("channel not found")
	}

	delete(s.channels, channelID)
	s.mu.Unlock()

	// Déconnecter tous les clients du channel
	channel.mu.Lock()
	for client := range channel.clients {
		client.mu.Lock()
		delete(client.channels, channelID)
		client.mu.Unlock()

		// Envoyer une notification de fermeture
		closeMsg := &Message{
			ID:        generateID(),
			Type:      MessageTypeSystem,
			Channel:   channelID,
			Data:      map[string]interface{}{"action": "channel_closed"},
			Timestamp: time.Now(),
		}
		client.Send(closeMsg)
	}
	channel.mu.Unlock()

	s.logger.Info("Channel closed", "channel_id", channelID)
	return nil
}

// Types pour l'API

type ChannelInfo struct {
	ID          string           `json:"id"`
	Namespace   string           `json:"namespace"`
	ClientCount int              `json:"client_count"`
	Settings    *ChannelSettings `json:"settings"`
	CreatedAt   time.Time        `json:"created_at"`
}

type ClientInfo struct {
	ID        string    `json:"id"`
	User      *UserInfo `json:"user"`
	Channels  []string  `json:"channels"`
	LastPing  time.Time `json:"last_ping"`
	Connected time.Time `json:"connected"`
}

type ServerStats struct {
	TotalChannels  int                        `json:"total_channels"`
	TotalClients   int                        `json:"total_clients"`
	NamespaceStats map[string]*NamespaceStats `json:"namespace_stats"`
	Uptime         time.Duration              `json:"uptime" swaggertype:"integer" format:"int64" example:"3600000000000"`
	LastUpdated    time.Time                  `json:"last_updated"`
}

type NamespaceStats struct {
	Channels int `json:"channels"`
	Clients  int `json:"clients"`
}

// Helper pour valider les channels
func ValidateChannelID(channelID string) error {
	if channelID == "" {
		return fmt.Errorf("channel ID cannot be empty")
	}

	parts := strings.SplitN(channelID, ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("channel ID must follow format 'namespace:identifier'")
	}

	namespace := parts[0]
	identifier := parts[1]

	if namespace == "" || identifier == "" {
		return fmt.Errorf("namespace and identifier cannot be empty")
	}

	// Valider les namespaces autorisés
	allowedNamespaces := []string{"chat", "stream", "notifications", "private"}
	valid := false
	for _, allowed := range allowedNamespaces {
		if namespace == allowed {
			valid = true
			break
		}
	}

	if !valid {
		return fmt.Errorf("invalid namespace: %s", namespace)
	}

	return nil
}
