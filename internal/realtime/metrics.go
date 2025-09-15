package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Logger interface for logging operations
type Logger interface {
	Info(msg string, args ...interface{})
	Warn(msg string, args ...interface{})
	Error(msg string, args ...interface{})
}

// MetricsCollector collecte les métriques du système de chat
type MetricsCollector struct {
	rdb    *redis.Client
	logger Logger
	
	// Métriques en mémoire pour performance
	mu              sync.RWMutex
	connectionCount int64
	messageCount    int64
	lastUpdate      time.Time
	
	// Canal pour les événements
	events chan *MetricEvent
}

type MetricEvent struct {
	Type      MetricType             `json:"type"`
	Channel   string                 `json:"channel,omitempty"`
	UserID    string                 `json:"user_id,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

type MetricType string

const (
	MetricConnectionOpened   MetricType = "connection_opened"
	MetricConnectionClosed   MetricType = "connection_closed"
	MetricChannelSubscribed  MetricType = "channel_subscribed"
	MetricChannelUnsubscribed MetricType = "channel_unsubscribed"
	MetricMessageSent        MetricType = "message_sent"
	MetricMessageReceived    MetricType = "message_received"
	MetricError              MetricType = "error"
	MetricSlowOperation      MetricType = "slow_operation"
)

type RealtimeMetrics struct {
	Connections struct {
		Total   int64 `json:"total"`
		Active  int64 `json:"active"`
		Peak    int64 `json:"peak"`
	} `json:"connections"`
	
	Channels struct {
		Total   int64 `json:"total"`
		Active  int64 `json:"active"`
		Peak    int64 `json:"peak"`
	} `json:"channels"`
	
	Messages struct {
		Total       int64   `json:"total"`
		PerSecond   float64 `json:"per_second"`
		PerMinute   float64 `json:"per_minute"`
		Peak        int64   `json:"peak"`
	} `json:"messages"`
	
	Performance struct {
		AvgLatency    time.Duration `json:"avg_latency"`
		MaxLatency    time.Duration `json:"max_latency"`
		SlowOps       int64         `json:"slow_operations"`
		ErrorRate     float64       `json:"error_rate"`
	} `json:"performance"`
	
	Memory struct {
		ChannelsSize   int64 `json:"channels_size"`
		ClientsSize    int64 `json:"clients_size"`
		MessagesSize   int64 `json:"messages_size"`
	} `json:"memory"`
	
	LastUpdated time.Time `json:"last_updated"`
}

type ChannelMetrics struct {
	ID              string        `json:"id"`
	Namespace       string        `json:"namespace"`
	ClientCount     int64         `json:"client_count"`
	MessageCount    int64         `json:"message_count"`
	MessagesPerMin  float64       `json:"messages_per_minute"`
	AvgMessageSize  float64       `json:"avg_message_size"`
	LastActivity    time.Time     `json:"last_activity"`
	CreatedAt       time.Time     `json:"created_at"`
	Uptime          time.Duration `json:"uptime"`
}

func NewMetricsCollector(rdb *redis.Client, logger Logger) *MetricsCollector {
	mc := &MetricsCollector{
		rdb:     rdb,
		logger:  logger,
		events:  make(chan *MetricEvent, 1000),
	}
	
	// Démarrer le collecteur d'événements
	go mc.processEvents()
	
	// Démarrer l'agrégation périodique
	go mc.periodicAggregation()
	
	return mc
}

// ============================================================================
// ENREGISTREMENT D'ÉVÉNEMENTS
// ============================================================================

func (mc *MetricsCollector) RecordEvent(event *MetricEvent) {
	event.Timestamp = time.Now()
	
	select {
	case mc.events <- event:
	default:
		// Canal plein, ignorer l'événement
		mc.logger.Warn("Metrics event channel full, dropping event", "type", event.Type)
	}
}

func (mc *MetricsCollector) RecordConnection(userID string, connected bool) {
	eventType := MetricConnectionOpened
	if !connected {
		eventType = MetricConnectionClosed
	}
	
	mc.RecordEvent(&MetricEvent{
		Type:   eventType,
		UserID: userID,
	})
}

func (mc *MetricsCollector) RecordChannelActivity(channelID, userID string, subscribed bool) {
	eventType := MetricChannelSubscribed
	if !subscribed {
		eventType = MetricChannelUnsubscribed
	}
	
	mc.RecordEvent(&MetricEvent{
		Type:    eventType,
		Channel: channelID,
		UserID:  userID,
	})
}

func (mc *MetricsCollector) RecordMessage(channelID, userID string, messageSize int) {
	mc.RecordEvent(&MetricEvent{
		Type:    MetricMessageSent,
		Channel: channelID,
		UserID:  userID,
		Data: map[string]interface{}{
			"size": messageSize,
		},
	})
}

func (mc *MetricsCollector) RecordError(channelID, userID, errorType string) {
	mc.RecordEvent(&MetricEvent{
		Type:    MetricError,
		Channel: channelID,
		UserID:  userID,
		Data: map[string]interface{}{
			"error_type": errorType,
		},
	})
}

func (mc *MetricsCollector) RecordSlowOperation(operation string, duration time.Duration) {
	mc.RecordEvent(&MetricEvent{
		Type: MetricSlowOperation,
		Data: map[string]interface{}{
			"operation": operation,
			"duration":  duration.Milliseconds(),
		},
	})
}

// ============================================================================
// TRAITEMENT DES ÉVÉNEMENTS
// ============================================================================

func (mc *MetricsCollector) processEvents() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	
	batch := make([]*MetricEvent, 0, 100)
	
	for {
		select {
		case event := <-mc.events:
			batch = append(batch, event)
			
			// Traiter par batch de 100 ou toutes les secondes
			if len(batch) >= 100 {
				mc.processBatch(batch)
				batch = batch[:0]
			}
			
		case <-ticker.C:
			if len(batch) > 0 {
				mc.processBatch(batch)
				batch = batch[:0]
			}
		}
	}
}

func (mc *MetricsCollector) processBatch(events []*MetricEvent) {
	ctx := context.Background()
	pipe := mc.rdb.Pipeline()
	
	now := time.Now()
	hourKey := now.Format("2006010215") // YYYYMMDDHH
	dayKey := now.Format("20060102")    // YYYYMMDD
	
	for _, event := range events {
		// Métriques globales
		pipe.Incr(ctx, fmt.Sprintf("metrics:events:%s:total", event.Type))
		pipe.Incr(ctx, fmt.Sprintf("metrics:events:%s:hour:%s", event.Type, hourKey))
		pipe.Incr(ctx, fmt.Sprintf("metrics:events:%s:day:%s", event.Type, dayKey))
		
		// Métriques par channel
		if event.Channel != "" {
			pipe.Incr(ctx, fmt.Sprintf("metrics:channels:%s:events:%s", event.Channel, event.Type))
			pipe.Set(ctx, fmt.Sprintf("metrics:channels:%s:last_activity", event.Channel), now.Unix(), 24*time.Hour)
		}
		
		// Traitement spécifique par type
		switch event.Type {
		case MetricConnectionOpened:
			mc.mu.Lock()
			mc.connectionCount++
			mc.mu.Unlock()
			pipe.Incr(ctx, "metrics:connections:current")
			
		case MetricConnectionClosed:
			mc.mu.Lock()
			mc.connectionCount--
			mc.mu.Unlock()
			pipe.Decr(ctx, "metrics:connections:current")
			
		case MetricChannelSubscribed:
			pipe.Incr(ctx, fmt.Sprintf("metrics:channels:%s:subscribers", event.Channel))
			
		case MetricChannelUnsubscribed:
			pipe.Decr(ctx, fmt.Sprintf("metrics:channels:%s:subscribers", event.Channel))
			
		case MetricMessageSent:
			mc.mu.Lock()
			mc.messageCount++
			mc.mu.Unlock()
			
			if size, ok := event.Data["size"].(int); ok {
				pipe.IncrBy(ctx, "metrics:messages:total_size", int64(size))
				pipe.IncrBy(ctx, fmt.Sprintf("metrics:channels:%s:message_size", event.Channel), int64(size))
			}
			
		case MetricError:
			if errorType, ok := event.Data["error_type"].(string); ok {
				pipe.Incr(ctx, fmt.Sprintf("metrics:errors:%s", errorType))
			}
			
		case MetricSlowOperation:
			if duration, ok := event.Data["duration"].(int64); ok {
				pipe.IncrBy(ctx, "metrics:performance:slow_ops_total_time", duration)
			}
		}
	}
	
	// Exécuter le pipeline
	_, err := pipe.Exec(ctx)
	if err != nil {
		mc.logger.Error("Failed to save metrics to Redis", "error", err)
	}
	
	// Mettre à jour les métriques en mémoire
	mc.mu.Lock()
	mc.lastUpdate = now
	mc.mu.Unlock()
}

// ============================================================================
// RÉCUPÉRATION DES MÉTRIQUES
// ============================================================================

func (mc *MetricsCollector) GetRealtimeMetrics(ctx context.Context) (*RealtimeMetrics, error) {
	metrics := &RealtimeMetrics{
		LastUpdated: time.Now(),
	}
	
	// Connexions
	currentConns, _ := mc.rdb.Get(ctx, "metrics:connections:current").Int64()
	totalConns, _ := mc.rdb.Get(ctx, "metrics:events:connection_opened:total").Int64()
	peakConns, _ := mc.rdb.Get(ctx, "metrics:connections:peak").Int64()
	
	metrics.Connections.Active = currentConns
	metrics.Connections.Total = totalConns
	metrics.Connections.Peak = peakConns
	
	// Messages
	totalMessages, _ := mc.rdb.Get(ctx, "metrics:events:message_sent:total").Int64()
	
	// Calculer les messages par seconde/minute
	now := time.Now()
	lastMinuteKey := now.Add(-time.Minute).Format("2006010215")
	lastMinuteMessages, _ := mc.rdb.Get(ctx, fmt.Sprintf("metrics:events:message_sent:hour:%s", lastMinuteKey)).Int64()
	
	metrics.Messages.Total = totalMessages
	metrics.Messages.PerMinute = float64(lastMinuteMessages)
	metrics.Messages.PerSecond = float64(lastMinuteMessages) / 60.0
	
	// Channels actifs
	channelKeys, _ := mc.rdb.Keys(ctx, "metrics:channels:*:subscribers").Result()
	activeChannels := int64(0)
	for _, key := range channelKeys {
		subs, _ := mc.rdb.Get(ctx, key).Int64()
		if subs > 0 {
			activeChannels++
		}
	}
	metrics.Channels.Active = activeChannels
	metrics.Channels.Total = int64(len(channelKeys))
	
	// Performance
	slowOps, _ := mc.rdb.Get(ctx, "metrics:events:slow_operation:total").Int64()
	totalErrors, _ := mc.rdb.Get(ctx, "metrics:events:error:total").Int64()
	
	metrics.Performance.SlowOps = slowOps
	if totalMessages > 0 {
		metrics.Performance.ErrorRate = float64(totalErrors) / float64(totalMessages) * 100
	}
	
	return metrics, nil
}

func (mc *MetricsCollector) GetChannelMetrics(ctx context.Context, channelID string) (*ChannelMetrics, error) {
	metrics := &ChannelMetrics{
		ID: channelID,
	}
	
	// Subscribers actuels
	subscribers, _ := mc.rdb.Get(ctx, fmt.Sprintf("metrics:channels:%s:subscribers", channelID)).Int64()
	metrics.ClientCount = subscribers
	
	// Messages total
	messages, _ := mc.rdb.Get(ctx, fmt.Sprintf("metrics:channels:%s:events:message_sent", channelID)).Int64()
	metrics.MessageCount = messages
	
	// Taille moyenne des messages
	totalSize, _ := mc.rdb.Get(ctx, fmt.Sprintf("metrics:channels:%s:message_size", channelID)).Int64()
	if messages > 0 {
		metrics.AvgMessageSize = float64(totalSize) / float64(messages)
	}
	
	// Dernière activité
	lastActivity, _ := mc.rdb.Get(ctx, fmt.Sprintf("metrics:channels:%s:last_activity", channelID)).Int64()
	if lastActivity > 0 {
		metrics.LastActivity = time.Unix(lastActivity, 0)
	}
	
	// Messages par minute (dernière heure)
	now := time.Now()
	hourKey := now.Format("2006010215")
	lastHourMessages, _ := mc.rdb.Get(ctx, fmt.Sprintf("metrics:channels:%s:events:message_sent:hour:%s", channelID, hourKey)).Int64()
	metrics.MessagesPerMin = float64(lastHourMessages) / 60.0
	
	return metrics, nil
}

func (mc *MetricsCollector) GetTopChannels(ctx context.Context, limit int) ([]*ChannelMetrics, error) {
	// Récupérer tous les channels avec des subscribers
	channelKeys, err := mc.rdb.Keys(ctx, "metrics:channels:*:subscribers").Result()
	if err != nil {
		return nil, err
	}
	
	type channelScore struct {
		ID    string
		Score int64
	}
	
	channels := make([]channelScore, 0, len(channelKeys))
	
	for _, key := range channelKeys {
		// Extraire l'ID du channel
		parts := strings.Split(key, ":")
		if len(parts) >= 3 {
			channelID := strings.Join(parts[2:len(parts)-1], ":")
			
			// Score basé sur le nombre de subscribers + activité récente
			subscribers, _ := mc.rdb.Get(ctx, key).Int64()
			lastActivity, _ := mc.rdb.Get(ctx, fmt.Sprintf("metrics:channels:%s:last_activity", channelID)).Int64()
			
			score := subscribers * 100
			if lastActivity > 0 {
				// Bonus pour activité récente (dernière heure)
				if time.Since(time.Unix(lastActivity, 0)) < time.Hour {
					score += 50
				}
			}
			
			channels = append(channels, channelScore{
				ID:    channelID,
				Score: score,
			})
		}
	}
	
	// Trier par score
	sort.Slice(channels, func(i, j int) bool {
		return channels[i].Score > channels[j].Score
	})
	
	// Limiter les résultats
	if limit > 0 && len(channels) > limit {
		channels = channels[:limit]
	}
	
	// Récupérer les métriques détaillées
	result := make([]*ChannelMetrics, 0, len(channels))
	for _, ch := range channels {
		metrics, err := mc.GetChannelMetrics(ctx, ch.ID)
		if err == nil {
			result = append(result, metrics)
		}
	}
	
	return result, nil
}

// ============================================================================
// AGRÉGATION PÉRIODIQUE
// ============================================================================

func (mc *MetricsCollector) periodicAggregation() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	
	for range ticker.C {
		mc.aggregateMetrics()
	}
}

func (mc *MetricsCollector) aggregateMetrics() {
	ctx := context.Background()
	
	// Nettoyer les anciennes métriques (plus de 7 jours)
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	cutoffKey := cutoff.Format("20060102")
	
	// Supprimer les métriques par jour anciennes
	oldKeys, _ := mc.rdb.Keys(ctx, "metrics:*:day:*").Result()
	for _, key := range oldKeys {
		parts := strings.Split(key, ":")
		if len(parts) >= 4 {
			dayKey := parts[len(parts)-1]
			if dayKey < cutoffKey {
				mc.rdb.Del(ctx, key)
			}
		}
	}
	
	// Calculer et sauvegarder les pics
	mc.updatePeakMetrics(ctx)
	
	mc.logger.Info("Metrics aggregation completed")
}

func (mc *MetricsCollector) updatePeakMetrics(ctx context.Context) {
	// Peak connections
	currentConns, _ := mc.rdb.Get(ctx, "metrics:connections:current").Int64()
	peakConns, _ := mc.rdb.Get(ctx, "metrics:connections:peak").Int64()
	
	if currentConns > peakConns {
		mc.rdb.Set(ctx, "metrics:connections:peak", currentConns, 0)
	}
	
	// Peak messages per minute
	now := time.Now()
	currentHourKey := now.Format("2006010215")
	currentHourMessages, _ := mc.rdb.Get(ctx, fmt.Sprintf("metrics:events:message_sent:hour:%s", currentHourKey)).Int64()
	peakMessages, _ := mc.rdb.Get(ctx, "metrics:messages:peak_per_hour").Int64()
	
	if currentHourMessages > peakMessages {
		mc.rdb.Set(ctx, "metrics:messages:peak_per_hour", currentHourMessages, 0)
	}
}

// ============================================================================
// EXPORT DES MÉTRIQUES
// ============================================================================

func (mc *MetricsCollector) ExportMetrics(ctx context.Context, format string) ([]byte, error) {
	metrics, err := mc.GetRealtimeMetrics(ctx)
	if err != nil {
		return nil, err
	}
	
	switch format {
	case "json":
		return json.MarshalIndent(metrics, "", "  ")
	case "prometheus":
		return mc.exportPrometheusMetrics(ctx, metrics)
	default:
		return nil, fmt.Errorf("unsupported format: %s", format)
	}
}

func (mc *MetricsCollector) exportPrometheusMetrics(ctx context.Context, metrics *RealtimeMetrics) ([]byte, error) {
	var output strings.Builder
	
	// Connexions
	output.WriteString("# HELP realtime_connections_active Number of active connections\n")
	output.WriteString("# TYPE realtime_connections_active gauge\n")
	output.WriteString(fmt.Sprintf("realtime_connections_active %d\n", metrics.Connections.Active))
	
	output.WriteString("# HELP realtime_connections_total Total number of connections\n")
	output.WriteString("# TYPE realtime_connections_total counter\n")
	output.WriteString(fmt.Sprintf("realtime_connections_total %d\n", metrics.Connections.Total))
	
	// Messages
	output.WriteString("# HELP realtime_messages_total Total number of messages\n")
	output.WriteString("# TYPE realtime_messages_total counter\n")
	output.WriteString(fmt.Sprintf("realtime_messages_total %d\n", metrics.Messages.Total))
	
	output.WriteString("# HELP realtime_messages_per_second Messages per second\n")
	output.WriteString("# TYPE realtime_messages_per_second gauge\n")
	output.WriteString(fmt.Sprintf("realtime_messages_per_second %.2f\n", metrics.Messages.PerSecond))
	
	// Channels
	output.WriteString("# HELP realtime_channels_active Number of active channels\n")
	output.WriteString("# TYPE realtime_channels_active gauge\n")
	output.WriteString(fmt.Sprintf("realtime_channels_active %d\n", metrics.Channels.Active))
	
	// Performance
	output.WriteString("# HELP realtime_slow_operations_total Number of slow operations\n")
	output.WriteString("# TYPE realtime_slow_operations_total counter\n")
	output.WriteString(fmt.Sprintf("realtime_slow_operations_total %d\n", metrics.Performance.SlowOps))

	output.WriteString("# HELP realtime_error_rate Error rate percentage\n")
	output.WriteString("# TYPE realtime_error_rate gauge\n")
	output.WriteString(fmt.Sprintf("realtime_error_rate %.2f\n", metrics.Performance.ErrorRate))
	
	return []byte(output.String()), nil
}