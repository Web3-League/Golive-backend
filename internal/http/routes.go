// api/internal/http/routes.go
package httpapi

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-playground/validator/v10"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	docs "golive/docs"
	"golive/internal/config"
	"golive/internal/http/handlers"
	mw "golive/internal/middleware"
	"golive/pkg/logger"
)

type Server struct {
	DB       *pgxpool.Pool
	RDB      *redis.Client
	Config   *config.Config
	Logger   *logger.Logger
	Validate *validator.Validate

	// Handlers
	System   *handlers.SystemHandler
	Auth     *handlers.AuthHandler
	Users    *handlers.UsersHandler
	Streams  *handlers.StreamsHandler
	Realtime *handlers.RealtimeHandler
}

func NewServer(db *pgxpool.Pool, rdb *redis.Client, cfg *config.Config, log *logger.Logger) *Server {
	s := &Server{
		DB:       db,
		RDB:      rdb,
		Config:   cfg,
		Logger:   log,
		Validate: validator.New(),
	}

	// Configure Swagger documentation host dynamically
	host := cfg.Server.Host
	if host == "" || host == "0.0.0.0" {
		host = "localhost"
	}
	docs.SwaggerInfo.Host = fmt.Sprintf("%s:%d", host, cfg.Server.Port)
	docs.SwaggerInfo.BasePath = "/api"

	// Initialiser les handlers
	s.System = handlers.NewSystemHandler(s.DB, s.RDB, s.Logger)
	s.Auth = handlers.NewAuthHandler(s.DB, s.Config, s.Logger, s.Validate)
	s.Users = handlers.NewUsersHandler(s.DB, s.Logger)
	s.Streams = handlers.NewStreamsHandler(s.DB, s.Config, s.Logger, s.Validate, nil)
	s.Realtime = handlers.NewRealtimeHandler(s.DB, s.RDB, s.Logger)

	return s
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()

	// Global middleware
	r.Use(middleware.RequestID)
	r.Use(mw.Logger(s.Logger))
	r.Use(mw.Recovery(s.Logger))
	r.Use(mw.Security())
	r.Use(mw.CORS(s.Config.CORS))
	r.Use(mw.RateLimit(s.RDB, s.Config.RateLimit))
	r.Use(mw.LimitRequestSize(1024 * 1024))

	// Swagger documentation
	r.Get("/swagger", s.swaggerUIHandler())
	r.Get("/swagger/doc.json", s.swaggerDocHandler())

	// API routes
	r.Route("/api", func(r chi.Router) {
		// System routes
		r.Get("/health", s.System.HandleHealth)
		r.Get("/metrics", s.System.HandleMetrics)
		r.Get("/endpoints", s.System.HandleListEndpoints(r))

		// Public routes
		r.Group(func(r chi.Router) {
			// Auth (POST avec ContentType)
			r.Group(func(r chi.Router) {
				r.Use(mw.ContentType("application/json"))
				r.Post("/auth/register", s.Auth.HandleRegister)
				r.Post("/auth/login", s.Auth.HandleLogin)
				r.Post("/auth/refresh", s.Auth.HandleRefreshToken)
			})

			// Public streams (GET sans ContentType)
			r.Get("/streams", s.Streams.HandleListStreams)
			r.Get("/streams/{key}", s.Streams.HandleGetStream)
			r.Get("/streams/{key}/analytics", s.Streams.HandleStreamAnalytics)
			r.Get("/categories", s.Streams.HandleListCategories)

		})

		// Protected routes
		r.Group(func(r chi.Router) {
			r.Use(mw.Auth(s.Config.JWT.Secret))

			// Auth protected
			r.Get("/auth/me", s.Auth.HandleGetCurrentUser)
			r.Group(func(r chi.Router) {
				r.Use(mw.ContentType("application/json"))
				r.Post("/auth/logout", s.Auth.HandleLogout)
			})

			// Users
			s.setupUserRoutes(r)
			s.setupStreamRoutes(r)
			s.setupRealtimeRoutes(r)
		})

		// WebSocket routes (auth optionnelle)
		r.Group(func(r chi.Router) {
			r.Use(mw.OptionalAuth(s.Config.JWT.Secret))

			// WebSocket générique pour tous les channels
			r.Get("/ws", s.Realtime.HandleWebSocket)
		})
	})

	return r
}

func (s *Server) setupUserRoutes(r chi.Router) {
	r.Route("/users", func(r chi.Router) {
		// GET routes
		r.Get("/me", s.Users.HandleGetCurrentUser)
		r.Get("/me/streams", s.Users.HandleGetUserStreams)
		r.Get("/me/following", s.Users.HandleGetFollowing)
		r.Get("/me/followers", s.Users.HandleGetFollowers)

		// POST/PUT/DELETE avec ContentType
		r.Group(func(r chi.Router) {
			r.Use(mw.ContentType("application/json"))
			r.Put("/me", s.Users.HandleUpdateCurrentUser)
			r.Delete("/me", s.Users.HandleDeleteCurrentUser)
			r.Post("/{userID}/follow", s.Users.HandleFollowUser)
			r.Delete("/{userID}/follow", s.Users.HandleUnfollowUser)
		})
	})
}

func (s *Server) setupStreamRoutes(r chi.Router) {
	r.Route("/me/streams", func(r chi.Router) {
		// GET routes
		r.Get("/", s.Streams.HandleGetUserStreams)

		// POST/PUT/DELETE avec ContentType
		r.Group(func(r chi.Router) {
			r.Use(mw.ContentType("application/json"))
			r.Post("/", s.Streams.HandleCreateStream)
			r.Put("/{key}", s.Streams.HandleUpdateStream)
			r.Delete("/{key}", s.Streams.HandleDeleteStream)
		})
		r.Post("/{key}/start", s.Streams.HandleStartStream)
		r.Post("/{key}/stop", s.Streams.HandleStopStream)
		r.Post("/{key}/rotate", s.Streams.HandleRotateIngestKey)
	})
}

func (s *Server) setupRealtimeRoutes(r chi.Router) {
	r.Route("/realtime", func(r chi.Router) {
		// Administration et monitoring
		r.Get("/stats", s.Realtime.HandleGetStats)
		r.Get("/channels", s.Realtime.HandleGetChannelList)

		// Gestion des channels
		r.Route("/channels/{channel}", func(r chi.Router) {
			// Informations du channel
			r.Get("/", s.Realtime.HandleGetChannelInfo)
			r.Get("/history", s.Realtime.HandleGetHistory)
			r.Get("/presence", s.Realtime.HandleGetPresence)

			// Actions sur le channel (avec ContentType)
			r.Group(func(r chi.Router) {
				r.Use(mw.ContentType("application/json"))

				// Publier un message
				r.Post("/publish", s.Realtime.HandlePublishMessage)

				// Broadcast système
				r.Post("/broadcast", s.Realtime.HandleBroadcastToChannel)

				// Modération
				r.Delete("/messages/{messageID}", s.Realtime.HandleDeleteMessage)
				r.Post("/clear", s.Realtime.HandleClearChannel)
				r.Post("/users/{userID}/timeout", s.Realtime.HandleTimeoutUser)
				r.Post("/users/{userID}/ban", s.Realtime.HandleBanUser)
				r.Delete("/users/{userID}/ban", s.Realtime.HandleUnbanUser)
				r.Put("/settings", s.Realtime.HandleUpdateChannelSettings)
				r.Post("/access", s.Realtime.HandleGrantChannelAccess)
				r.Delete("/access/{userID}", s.Realtime.HandleRevokeChannelAccess)
				r.Post("/presence/sync", s.Realtime.HandleSyncPresence)
			})
		})

		// RPC endpoints
		r.Group(func(r chi.Router) {
			r.Use(mw.ContentType("application/json"))
			r.Post("/rpc", s.Realtime.HandleRPC)
		})
	})
}

func (s *Server) swaggerUIHandler() http.HandlerFunc {
	docURL := "/swagger/doc.json"
	page := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>GoLive API Docs</title>
    <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
</head>
<body>
    <div id="swagger-ui"></div>
    <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
    <script>
        window.onload = function() {
            window.ui = SwaggerUIBundle({
                url: '%s',
                dom_id: '#swagger-ui',
                presets: [SwaggerUIBundle.presets.apis],
                layout: 'BaseLayout'
            });
        };
    </script>
</body>
</html>`, docURL)

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, page)
	}
}

func (s *Server) swaggerDocHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, docs.SwaggerInfo.ReadDoc())
	}
}
