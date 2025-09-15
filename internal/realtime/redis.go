package realtime

// api/internal/realtime/redis.go

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisManager gère la persistance Redis pour le chat temps réel
type RedisManager struct {
	rdb    *redis.Client
	logger Logger
}

func NewRedisManager(rdb *redis.Client, logger Logger) *RedisManager {
	return &RedisManager{
		rdb:    rdb,
		logger: logger,
	}
}

// ============================================================================
// GESTION DES MESSAGES
// ============================================================================

// SaveMessage sauvegarde un message en Redis avec TTL
func (r *RedisManager) SaveMessage(ctx context.Context, channelID string, message *Message) error {
	key := fmt.Sprintf("chat:messages:%s", channelID)

	// Sérialiser le message
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	// Ajouter à la liste Redis avec score basé sur timestamp
	score := float64(message.Timestamp.UnixNano())

	pipe := r.rdb.Pipeline()

	// Ajouter le message à la sorted set
	pipe.ZAdd(ctx, key, redis.Z{
		Score:  score,
		Member: data,
	})

	// Limiter à 1000 messages par channel
	pipe.ZRemRangeByRank(ctx, key, 0, -1001)

	// Définir une expiration de 24h pour la clé
	pipe.Expire(ctx, key, 24*time.Hour)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to save message to Redis: %w", err)
	}

	r.logger.Info("Message saved to Redis", "channel", channelID, "message_id", message.ID)
	return nil
}

// GetMessages récupère les messages d'un channel depuis Redis
func (r *RedisManager) GetMessages(ctx context.Context, channelID string, limit int, since string) ([]*Message, error) {
	key := fmt.Sprintf("chat:messages:%s", channelID)

	var start int64 = 0
	if since != "" {
		// Si un message ID est fourni, commencer après ce message
		sinceScore, err := r.getMessageScore(ctx, channelID, since)
		if err == nil {
			start = int64(sinceScore) + 1
		}
	}

	// Récupérer les messages depuis Redis
	results, err := r.rdb.ZRangeByScoreWithScores(ctx, key, &redis.ZRangeBy{
		Min:   fmt.Sprintf("%d", start),
		Max:   "+inf",
		Count: int64(limit),
	}).Result()

	if err != nil {
		return nil, fmt.Errorf("failed to get messages from Redis: %w", err)
	}

	messages := make([]*Message, 0, len(results))
	for _, result := range results {
		var msg Message
		if err := json.Unmarshal([]byte(result.Member.(string)), &msg); err != nil {
			r.logger.Error("Failed to unmarshal message", "error", err)
			continue
		}
		messages = append(messages, &msg)
	}

	return messages, nil
}

// DeleteMessage supprime un message spécifique
func (r *RedisManager) DeleteMessage(ctx context.Context, channelID, messageID string) error {
	key := fmt.Sprintf("chat:messages:%s", channelID)

	// Récupérer tous les messages pour trouver celui à supprimer
	results, err := r.rdb.ZRangeWithScores(ctx, key, 0, -1).Result()
	if err != nil {
		return fmt.Errorf("failed to get messages: %w", err)
	}

	for _, result := range results {
		var msg Message
		if err := json.Unmarshal([]byte(result.Member.(string)), &msg); err != nil {
			continue
		}

		if msg.ID == messageID {
			// Supprimer le message
			_, err := r.rdb.ZRem(ctx, key, result.Member).Result()
			if err != nil {
				return fmt.Errorf("failed to delete message: %w", err)
			}

			r.logger.Info("Message deleted from Redis", "channel", channelID, "message_id", messageID)
			return nil
		}
	}

	return fmt.Errorf("message not found")
}

// ClearMessages vide tous les messages d'un channel
func (r *RedisManager) ClearMessages(ctx context.Context, channelID string) error {
	key := fmt.Sprintf("chat:messages:%s", channelID)

	_, err := r.rdb.Del(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("failed to clear messages: %w", err)
	}

	r.logger.Info("Messages cleared from Redis", "channel", channelID)
	return nil
}

// ============================================================================
// GESTION DE LA PRÉSENCE
// ============================================================================

// SavePresence sauvegarde la présence d'un utilisateur
func (r *RedisManager) SavePresence(ctx context.Context, channelID string, userID string, presence *PresenceInfo) error {
	key := fmt.Sprintf("chat:presence:%s", channelID)

	data, err := json.Marshal(presence)
	if err != nil {
		return fmt.Errorf("failed to marshal presence: %w", err)
	}

	// Sauvegarder avec expiration de 5 minutes
	_, err = r.rdb.HSet(ctx, key, userID, data).Result()
	if err != nil {
		return fmt.Errorf("failed to save presence: %w", err)
	}

	// Définir expiration sur la clé
	r.rdb.Expire(ctx, key, 5*time.Minute)

	return nil
}

// GetPresence récupère la présence d'un channel
func (r *RedisManager) GetPresence(ctx context.Context, channelID string) (map[string]*PresenceInfo, error) {
	key := fmt.Sprintf("chat:presence:%s", channelID)

	results, err := r.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get presence: %w", err)
	}

	presence := make(map[string]*PresenceInfo)
	for userID, data := range results {
		var p PresenceInfo
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			r.logger.Error("Failed to unmarshal presence", "error", err)
			continue
		}
		presence[userID] = &p
	}

	return presence, nil
}

// RemovePresence supprime la présence d'un utilisateur
func (r *RedisManager) RemovePresence(ctx context.Context, channelID, userID string) error {
	key := fmt.Sprintf("chat:presence:%s", channelID)

	_, err := r.rdb.HDel(ctx, key, userID).Result()
	if err != nil {
		return fmt.Errorf("failed to remove presence: %w", err)
	}

	return nil
}

// UpdatePresenceLastSeen met à jour le last_seen d'un utilisateur
func (r *RedisManager) UpdatePresenceLastSeen(ctx context.Context, channelID, userID string) error {
	key := fmt.Sprintf("chat:presence:%s", channelID)

	// Récupérer la présence actuelle
	data, err := r.rdb.HGet(ctx, key, userID).Result()
	if err != nil {
		return err // Utilisateur pas présent
	}

	var presence PresenceInfo
	if err := json.Unmarshal([]byte(data), &presence); err != nil {
		return err
	}

	// Mettre à jour last_seen
	presence.LastSeen = time.Now()

	// Sauvegarder
	return r.SavePresence(ctx, channelID, userID, &presence)
}

// ============================================================================
// GESTION DES STATISTIQUES
// ============================================================================

// IncrementChannelStats incrémente les statistiques d'un channel
func (r *RedisManager) IncrementChannelStats(ctx context.Context, channelID, metric string, value int64) error {
	key := fmt.Sprintf("chat:stats:%s", channelID)

	_, err := r.rdb.HIncrBy(ctx, key, metric, value).Result()
	if err != nil {
		return fmt.Errorf("failed to increment stats: %w", err)
	}

	// Expiration de 24h
	r.rdb.Expire(ctx, key, 24*time.Hour)

	return nil
}

// GetChannelStats récupère les statistiques d'un channel
func (r *RedisManager) GetChannelStats(ctx context.Context, channelID string) (map[string]int64, error) {
	key := fmt.Sprintf("chat:stats:%s", channelID)

	results, err := r.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get stats: %w", err)
	}

	stats := make(map[string]int64)
	for metric, value := range results {
		if intValue, err := strconv.ParseInt(value, 10, 64); err == nil {
			stats[metric] = intValue
		}
	}

	return stats, nil
}

// ============================================================================
// GESTION DES BANS ET TIMEOUTS
// ============================================================================

// SaveBan sauvegarde un ban utilisateur
func (r *RedisManager) SaveBan(ctx context.Context, channelID, userID string, ban *BanInfo) error {
	key := fmt.Sprintf("chat:bans:%s:%s", channelID, userID)

	data, err := json.Marshal(ban)
	if err != nil {
		return fmt.Errorf("failed to marshal ban: %w", err)
	}

	// Ban permanent (pas d'expiration)
	_, err = r.rdb.Set(ctx, key, data, 0).Result()
	if err != nil {
		return fmt.Errorf("failed to save ban: %w", err)
	}

	r.logger.Info("User banned", "channel", channelID, "user", userID)
	return nil
}

// SaveTimeout sauvegarde un timeout utilisateur
func (r *RedisManager) SaveTimeout(ctx context.Context, channelID, userID string, timeout *TimeoutInfo, duration time.Duration) error {
	key := fmt.Sprintf("chat:timeouts:%s:%s", channelID, userID)

	data, err := json.Marshal(timeout)
	if err != nil {
		return fmt.Errorf("failed to marshal timeout: %w", err)
	}

	// Timeout avec expiration
	_, err = r.rdb.Set(ctx, key, data, duration).Result()
	if err != nil {
		return fmt.Errorf("failed to save timeout: %w", err)
	}

	r.logger.Info("User timed out", "channel", channelID, "user", userID, "duration", duration)
	return nil
}

// IsBanned vérifie si un utilisateur est banni
func (r *RedisManager) IsBanned(ctx context.Context, channelID, userID string) (bool, *BanInfo, error) {
	key := fmt.Sprintf("chat:bans:%s:%s", channelID, userID)

	data, err := r.rdb.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("failed to check ban: %w", err)
	}

	var ban BanInfo
	if err := json.Unmarshal([]byte(data), &ban); err != nil {
		return false, nil, fmt.Errorf("failed to unmarshal ban: %w", err)
	}

	return true, &ban, nil
}

// IsTimedOut vérifie si un utilisateur est en timeout
func (r *RedisManager) IsTimedOut(ctx context.Context, channelID, userID string) (bool, *TimeoutInfo, error) {
	key := fmt.Sprintf("chat:timeouts:%s:%s", channelID, userID)

	data, err := r.rdb.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("failed to check timeout: %w", err)
	}

	var timeout TimeoutInfo
	if err := json.Unmarshal([]byte(data), &timeout); err != nil {
		return false, nil, fmt.Errorf("failed to unmarshal timeout: %w", err)
	}

	return true, &timeout, nil
}

// RemoveBan supprime un ban
func (r *RedisManager) RemoveBan(ctx context.Context, channelID, userID string) error {
	key := fmt.Sprintf("chat:bans:%s:%s", channelID, userID)

	_, err := r.rdb.Del(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("failed to remove ban: %w", err)
	}

	r.logger.Info("User unbanned", "channel", channelID, "user", userID)
	return nil
}

func (r *RedisManager) GrantChannelAccess(ctx context.Context, channelID string, userIDs []string) error {
	if len(userIDs) == 0 {
		return nil
	}

	key := fmt.Sprintf("chat:access:%s", channelID)
	members := make([]interface{}, 0, len(userIDs))
	for _, id := range userIDs {
		if id == "" {
			continue
		}
		members = append(members, id)
	}

	if len(members) == 0 {
		return nil
	}

	if err := r.rdb.SAdd(ctx, key, members...).Err(); err != nil {
		return fmt.Errorf("failed to grant access: %w", err)
	}

	return nil
}

func (r *RedisManager) RevokeChannelAccess(ctx context.Context, channelID, userID string) error {
	key := fmt.Sprintf("chat:access:%s", channelID)
	if err := r.rdb.SRem(ctx, key, userID).Err(); err != nil {
		return fmt.Errorf("failed to revoke access: %w", err)
	}
	return nil
}

func (r *RedisManager) IsUserAuthorized(ctx context.Context, channelID, userID string) (bool, error) {
	key := fmt.Sprintf("chat:access:%s", channelID)
	exists, err := r.rdb.SIsMember(ctx, key, userID).Result()
	if err != nil {
		if err == redis.Nil {
			return false, nil
		}
		return false, fmt.Errorf("failed to check access: %w", err)
	}
	return exists, nil
}

func (r *RedisManager) SetPresenceSnapshot(ctx context.Context, channelID string, presence map[string]*PresenceInfo) error {
	key := fmt.Sprintf("chat:presence:%s", channelID)

	if err := r.rdb.Del(ctx, key).Err(); err != nil && err != redis.Nil {
		return fmt.Errorf("failed to clear presence: %w", err)
	}

	if len(presence) == 0 {
		return nil
	}

	data := make(map[string]interface{}, len(presence))
	for userID, info := range presence {
		if info == nil {
			continue
		}
		serialized, err := json.Marshal(info)
		if err != nil {
			continue
		}
		data[userID] = serialized
	}

	if len(data) == 0 {
		return nil
	}

	if err := r.rdb.HSet(ctx, key, data).Err(); err != nil {
		return fmt.Errorf("failed to set presence snapshot: %w", err)
	}

	r.rdb.Expire(ctx, key, 5*time.Minute)
	return nil
}

// ============================================================================
// HELPERS
// ============================================================================

func (r *RedisManager) getMessageScore(ctx context.Context, channelID, messageID string) (float64, error) {
	key := fmt.Sprintf("chat:messages:%s", channelID)

	results, err := r.rdb.ZRangeWithScores(ctx, key, 0, -1).Result()
	if err != nil {
		return 0, err
	}

	for _, result := range results {
		var msg Message
		if err := json.Unmarshal([]byte(result.Member.(string)), &msg); err != nil {
			continue
		}

		if msg.ID == messageID {
			return result.Score, nil
		}
	}

	return 0, fmt.Errorf("message not found")
}

// ============================================================================
// TYPES SUPPLÉMENTAIRES
// ============================================================================

type BanInfo struct {
	Reason   string    `json:"reason"`
	BannedBy string    `json:"banned_by"`
	BannedAt time.Time `json:"banned_at"`
}

type TimeoutInfo struct {
	Reason    string        `json:"reason"`
	Duration  time.Duration `json:"duration"`
	TimeoutBy string        `json:"timeout_by"`
	TimeoutAt time.Time     `json:"timeout_at"`
	ExpiresAt time.Time     `json:"expires_at"`
}

// ============================================================================
// MÉTHODES DE NETTOYAGE
// ============================================================================

// CleanupExpiredData nettoie les données expirées
func (r *RedisManager) CleanupExpiredData(ctx context.Context) error {
	// Nettoyer les présences inactives (plus de 5 minutes)
	pattern := "chat:presence:*"
	keys, err := r.rdb.Keys(ctx, pattern).Result()
	if err != nil {
		return err
	}

	for _, key := range keys {
		// Vérifier chaque utilisateur dans la présence
		users, err := r.rdb.HGetAll(ctx, key).Result()
		if err != nil {
			continue
		}

		for userID, data := range users {
			var presence PresenceInfo
			if err := json.Unmarshal([]byte(data), &presence); err != nil {
				continue
			}

			// Si last_seen > 5 minutes, supprimer
			if time.Since(presence.LastSeen) > 5*time.Minute {
				r.rdb.HDel(ctx, key, userID)
				r.logger.Info("Cleaned up expired presence", "key", key, "user", userID)
			}
		}

		// Si plus d'utilisateurs, supprimer la clé
		count, _ := r.rdb.HLen(ctx, key).Result()
		if count == 0 {
			r.rdb.Del(ctx, key)
		}
	}

	return nil
}

// GetGlobalStats retourne les statistiques globales
func (r *RedisManager) GetGlobalStats(ctx context.Context) (*GlobalStats, error) {
	// Compter les channels actifs
	channelKeys, err := r.rdb.Keys(ctx, "chat:presence:*").Result()
	if err != nil {
		return nil, err
	}

	totalChannels := int64(len(channelKeys))
	totalUsers := int64(0)

	// Compter les utilisateurs uniques
	uniqueUsers := make(map[string]bool)
	for _, key := range channelKeys {
		users, err := r.rdb.HKeys(ctx, key).Result()
		if err != nil {
			continue
		}

		for _, userID := range users {
			uniqueUsers[userID] = true
		}
	}

	totalUsers = int64(len(uniqueUsers))

	return &GlobalStats{
		ActiveChannels: totalChannels,
		ActiveUsers:    totalUsers,
		Timestamp:      time.Now(),
	}, nil
}

type GlobalStats struct {
	ActiveChannels int64     `json:"active_channels"`
	ActiveUsers    int64     `json:"active_users"`
	Timestamp      time.Time `json:"timestamp"`
}
