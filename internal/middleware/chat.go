// api/internal/middleware/chat.go
package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"golive/internal/realtime"
	"golive/pkg/logger"
	"golive/pkg/response"
)

// ChatRateLimit limite les messages de chat par utilisateur
func ChatRateLimit(rdb *redis.Client, maxMessages int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID := GetUserID(r.Context())
			if userID == "" {
				next.ServeHTTP(w, r)
				return
			}
			
			ctx := r.Context()
			key := fmt.Sprintf("chat_rate_limit:%s", userID)
			
			// Compter les messages dans la fenêtre
			now := time.Now().Unix()
			windowStart := now - int64(window.Seconds())
			
			pipe := rdb.Pipeline()
			
			// Supprimer les anciens messages
			pipe.ZRemRangeByScore(ctx, key, "0", fmt.Sprintf("%d", windowStart))
			
			// Compter les messages actuels
			pipe.ZCard(ctx, key)
			
			// Ajouter le message actuel
			pipe.ZAdd(ctx, key, redis.Z{Score: float64(now), Member: now})
			
			// Définir expiration
			pipe.Expire(ctx, key, window)
			
			results, err := pipe.Exec(ctx)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			
			count := results[1].(*redis.IntCmd).Val()
			if count >= int64(maxMessages) {
				response.Error(w, "Rate limit exceeded for chat messages", http.StatusTooManyRequests)
				return
			}
			
			next.ServeHTTP(w, r)
		})
	}
}

// ChatBanCheck vérifie si l'utilisateur est banni du chat
func ChatBanCheck(rdb *redis.Client, logger *logger.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID := GetUserID(r.Context())
			if userID == "" {
				next.ServeHTTP(w, r)
				return
			}
			
			// Extraire le channel ID depuis l'URL ou le body
			channelID := extractChannelIDFromRequest(r)
			if channelID == "" {
				next.ServeHTTP(w, r)
				return
			}
			
			ctx := r.Context()
			
			// Vérifier ban
			banKey := fmt.Sprintf("chat:bans:%s:%s", channelID, userID)
			banned, err := rdb.Exists(ctx, banKey).Result()
			if err != nil {
				logger.Error("Failed to check ban status", "error", err)
				next.ServeHTTP(w, r)
				return
			}
			
			if banned > 0 {
				response.Error(w, "You are banned from this chat", http.StatusForbidden)
				return
			}
			
			// Vérifier timeout
			timeoutKey := fmt.Sprintf("chat:timeouts:%s:%s", channelID, userID)
			timedOut, err := rdb.Exists(ctx, timeoutKey).Result()
			if err != nil {
				logger.Error("Failed to check timeout status", "error", err)
				next.ServeHTTP(w, r)
				return
			}
			
			if timedOut > 0 {
				// Récupérer les détails du timeout
				timeoutData, err := rdb.Get(ctx, timeoutKey).Result()
				if err == nil {
					var timeout realtime.TimeoutInfo
					if json.Unmarshal([]byte(timeoutData), &timeout) == nil {
						response.Error(w, fmt.Sprintf("You are timed out until %s. Reason: %s", 
							timeout.ExpiresAt.Format(time.RFC3339), timeout.Reason), http.StatusForbidden)
						return
					}
				}
				response.Error(w, "You are temporarily timed out from this chat", http.StatusForbidden)
				return
			}
			
			next.ServeHTTP(w, r)
		})
	}
}

// MessageContentFilter filtre le contenu des messages
func MessageContentFilter(logger *logger.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				next.ServeHTTP(w, r)
				return
			}
			
			// Lire le body une seule fois
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				response.Error(w, "Invalid JSON", http.StatusBadRequest)
				return
			}
			
			// Vérifier le contenu du message
			if content, exists := body["content"]; exists {
				if contentStr, ok := content.(string); ok {
					if err := validateMessageContent(contentStr); err != nil {
						response.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					
					// Nettoyer le contenu
					body["content"] = cleanMessageContent(contentStr)
				}
			}
			
			// Remettre le body nettoyé dans le contexte
			ctx := context.WithValue(r.Context(), "cleaned_body", body)
			r = r.WithContext(ctx)
			
			next.ServeHTTP(w, r)
		})
	}
}

// validateMessageContent valide le contenu d'un message
func validateMessageContent(content string) error {
	// Longueur maximum
	if len(content) > 500 {
		return fmt.Errorf("message too long (max 500 characters)")
	}
	
	// Vérifier si vide après nettoyage
	cleaned := strings.TrimSpace(content)
	if cleaned == "" {
		return fmt.Errorf("message cannot be empty")
	}
	
	// Filtres de contenu basiques
	lowerContent := strings.ToLower(cleaned)
	
	// Liste de mots interdits (exemple basique)
	forbiddenWords := []string{
		"spam", "hack", "cheat", // Ajoutez vos mots interdits
	}
	
	for _, word := range forbiddenWords {
		if strings.Contains(lowerContent, word) {
			return fmt.Errorf("message contains forbidden content")
		}
	}
	
	// Vérifier les URLs suspectes
	if strings.Contains(lowerContent, "http://") || strings.Contains(lowerContent, "https://") {
		if !isAllowedURL(cleaned) {
			return fmt.Errorf("message contains unauthorized links")
		}
	}
	
	return nil
}

// cleanMessageContent nettoie le contenu d'un message
func cleanMessageContent(content string) string {
	// Supprimer les caractères de contrôle
	cleaned := strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\r' && r != '\t' {
			return -1
		}
		return r
	}, content)
	
	// Limiter les sauts de ligne consécutifs
	for strings.Contains(cleaned, "\n\n\n") {
		cleaned = strings.ReplaceAll(cleaned, "\n\n\n", "\n\n")
	}
	
	// Supprimer les espaces en trop
	lines := strings.Split(cleaned, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	cleaned = strings.Join(lines, "\n")
	
	return strings.TrimSpace(cleaned)
}

// isAllowedURL vérifie si une URL est autorisée
func isAllowedURL(content string) bool {
	// Liste simple de domaines autorisés
	allowedDomains := []string{
		"youtube.com",
		"youtu.be",
		"twitch.tv",
		"github.com",
		// Ajoutez vos domaines autorisés
	}
	
	lowerContent := strings.ToLower(content)
	for _, domain := range allowedDomains {
		if strings.Contains(lowerContent, domain) {
			return true
		}
	}
	
	return false
}

// extractChannelIDFromRequest extrait l'ID du channel depuis la requête
func extractChannelIDFromRequest(r *http.Request) string {
	// Essayer d'extraire depuis l'URL path
	parts := strings.Split(r.URL.Path, "/")
	for i, part := range parts {
		if part == "streams" && i+1 < len(parts) && i+2 < len(parts) && parts[i+2] == "chat" {
			return fmt.Sprintf("chat:%s", parts[i+1])
		}
		if part == "channels" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	
	return ""
}

// ChatMetrics collecte les métriques de chat
func ChatMetrics(rdb *redis.Client, logger *logger.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			
			// Wrapper pour capturer le status code
			ww := &responseWriter{ResponseWriter: w, statusCode: 200}
			
			next.ServeHTTP(ww, r)
			
			// Enregistrer les métriques
			duration := time.Since(start)
			userID := GetUserID(r.Context())
			channelID := extractChannelIDFromRequest(r)
			
			// Métriques asynchrones
			go func() {
				ctx := context.Background()
				
				// Métriques générales
				incrementMetric(ctx, rdb, "chat:metrics:requests:total", 1)
				incrementMetric(ctx, rdb, fmt.Sprintf("chat:metrics:requests:status:%d", ww.statusCode), 1)
				incrementMetric(ctx, rdb, fmt.Sprintf("chat:metrics:requests:method:%s", r.Method), 1)
				
				// Métriques par channel
				if channelID != "" {
					incrementMetric(ctx, rdb, fmt.Sprintf("chat:metrics:channels:%s:requests", channelID), 1)
				}
				
				// Métriques de performance
				if duration > 1*time.Second {
					incrementMetric(ctx, rdb, "chat:metrics:slow_requests", 1)
				}
				
				// Log des requêtes importantes
				if ww.statusCode >= 400 {
					logger.Error("Chat request error",
						"method", r.Method,
						"path", r.URL.Path,
						"status", ww.statusCode,
						"duration", duration,
						"user_id", userID,
						"channel", channelID)
				}
			}()
		})
	}
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func incrementMetric(ctx context.Context, rdb *redis.Client, key string, value int64) {
	// Incrémenter la métrique
	rdb.IncrBy(ctx, key, value)
	
	// Définir une expiration de 24h
	rdb.Expire(ctx, key, 24*time.Hour)
}

// SpamDetection détecte le spam dans les messages
func SpamDetection(rdb *redis.Client, logger *logger.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID := GetUserID(r.Context())
			if userID == "" {
				next.ServeHTTP(w, r)
				return
			}
			
			ctx := r.Context()
			
			// Vérifier les messages identiques récents
			if body, exists := ctx.Value("cleaned_body").(map[string]interface{}); exists {
				if content, ok := body["content"].(string); ok {
					spamKey := fmt.Sprintf("chat:spam:%s", userID)
					
					// Récupérer les messages récents
					recent, err := rdb.LRange(ctx, spamKey, 0, 4).Result()
					if err == nil {
						// Compter les messages identiques
						duplicates := 0
						for _, msg := range recent {
							if msg == content {
								duplicates++
							}
						}
						
						if duplicates >= 3 {
							response.Error(w, "Spam detected: too many identical messages", http.StatusTooManyRequests)
							return
						}
					}
					
					// Ajouter le message à l'historique
					pipe := rdb.Pipeline()
					pipe.LPush(ctx, spamKey, content)
					pipe.LTrim(ctx, spamKey, 0, 9) // Garder les 10 derniers
					pipe.Expire(ctx, spamKey, 5*time.Minute)
					pipe.Exec(ctx)
				}
			}
			
			next.ServeHTTP(w, r)
		})
	}
}

// EmoteValidation valide les emotes dans les messages
func EmoteValidation() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if body, exists := r.Context().Value("cleaned_body").(map[string]interface{}); exists {
				if content, ok := body["content"].(string); ok {
					// Extraire et valider les emotes
					emotes := extractEmotes(content)
					if len(emotes) > 10 {
						response.Error(w, "Too many emotes in message", http.StatusBadRequest)
						return
					}
					
					// Ajouter les emotes validées au contexte
					body["emotes"] = emotes
					ctx := context.WithValue(r.Context(), "cleaned_body", body)
					r = r.WithContext(ctx)
				}
			}
			
			next.ServeHTTP(w, r)
		})
	}
}

// extractEmotes extrait les emotes d'un message
func extractEmotes(content string) []string {
	// Regex simple pour détecter les emotes (format :emote:)
	emotes := []string{}
	words := strings.Fields(content)
	
	for _, word := range words {
		if strings.HasPrefix(word, ":") && strings.HasSuffix(word, ":") && len(word) > 2 {
			emote := word[1 : len(word)-1]
			if isValidEmote(emote) {
				emotes = append(emotes, emote)
			}
		}
	}
	
	return emotes
}

// isValidEmote vérifie si un emote est valide
func isValidEmote(emote string) bool {
	// Liste simple d'emotes autorisées
	allowedEmotes := []string{
		"smile", "laugh", "cry", "heart", "thumbsup", "thumbsdown",
		"fire", "clap", "wave", "thinking", // Ajoutez vos emotes
	}
	
	for _, allowed := range allowedEmotes {
		if emote == allowed {
			return true
		}
	}
	
	return false
}