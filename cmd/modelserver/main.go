package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/modelserver/modelserver/internal/admin"
	"github.com/modelserver/modelserver/internal/auth"
	"github.com/modelserver/modelserver/internal/collector"
	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/crypto"
	"github.com/modelserver/modelserver/internal/modelcatalog"
	"github.com/modelserver/modelserver/internal/proxy"
	"github.com/modelserver/modelserver/internal/ratelimit"
	"github.com/modelserver/modelserver/internal/store"
)

// hydraIntrospectorAdapter adapts admin.HydraClient to the proxy.TokenIntrospector
// interface, bridging the two packages without creating an import cycle.
type hydraIntrospectorAdapter struct {
	client *admin.HydraClient
}

func (a *hydraIntrospectorAdapter) IntrospectToken(ctx context.Context, token string) (*proxy.TokenIntrospectResult, error) {
	res, err := a.client.IntrospectToken(ctx, token)
	if err != nil {
		return nil, err
	}
	return &proxy.TokenIntrospectResult{
		Active:   res.Active,
		Sub:      res.Sub,
		Ext:      res.Ext,
		ClientID: res.ClientID,
	}, nil
}

func main() {
	configPath := flag.String("config", "", "path to config file (default: config.yml)")
	flag.Parse()

	// Load config.
	var cfg *config.Config
	var err error
	if *configPath != "" {
		cfg, err = config.LoadFile(*configPath)
	} else if _, statErr := os.Stat("config.yml"); statErr == nil {
		cfg, err = config.LoadFile("config.yml")
	} else {
		cfg, err = config.Load(nil)
	}
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// Set up logger.
	var logLevel slog.Level
	switch cfg.Log.Level {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	var handler slog.Handler
	if cfg.Log.Format == "console" {
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})
	} else {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})
	}
	logger := slog.New(handler).With("component", "modelserver")

	// Connect to database.
	if cfg.DB.URL == "" {
		log.Fatal("database URL is required (db.url in config or MODELSERVER_DB_URL env)")
	}

	st, err := store.New(cfg.DB.URL, logger)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer st.Close()
	logger.Info("connected to database (pgxpool)")

	// Initialize encryption key.
	var encryptionKey []byte
	if cfg.Encryption.Key != "" {
		encryptionKey, err = crypto.ParseHexKey(cfg.Encryption.Key)
		if err != nil {
			log.Fatalf("invalid encryption key: %v", err)
		}
	}

	// Initialize collector.
	coll := collector.New(collector.Config{
		BatchSize:     cfg.Collector.BatchSize,
		FlushInterval: cfg.Collector.FlushInterval,
		BufferSize:    cfg.Collector.BufferSize,
	}, st, logger)
	coll.Start()
	defer coll.Stop()

	// Load upstreams, groups, routes, and the model catalog for the routing engine.
	upstreams, err := st.ListUpstreams()
	if err != nil {
		log.Fatalf("failed to load upstreams: %v", err)
	}
	if len(upstreams) > 0 && len(encryptionKey) == 0 {
		logger.Warn("encryption key not configured but upstreams exist — upstream API keys will not be decrypted")
	}
	groups, err := st.ListUpstreamGroupsWithMembers()
	if err != nil {
		log.Fatalf("failed to load upstream groups: %v", err)
	}
	routingRoutes, err := st.ListRoutes()
	if err != nil {
		log.Fatalf("failed to load routes: %v", err)
	}
	initialModels, err := st.ListModels()
	if err != nil {
		log.Fatalf("failed to load model catalog: %v", err)
	}
	catalog := modelcatalog.New(initialModels)

	oauthMgr := proxy.NewOAuthTokenManager(st, encryptionKey, logger)
	router := proxy.NewRouter(upstreams, groups, routingRoutes, encryptionKey, logger, cfg.Trace.SessionTTL, oauthMgr, catalog)
	router.StartSessionCleanup(10 * time.Minute)
	// Start health checker.
	router.HealthChecker().Start(context.Background())

	// Periodically reload routing configuration and the catalog together.
	// Both surfaces are independent atomic swaps — no cross-component lock.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			u, err := st.ListUpstreams()
			if err != nil {
				logger.Error("failed to reload upstreams", "error", err)
				continue
			}
			g, err := st.ListUpstreamGroupsWithMembers()
			if err != nil {
				logger.Error("failed to reload upstream groups", "error", err)
				continue
			}
			rt, err := st.ListRoutes()
			if err != nil {
				logger.Error("failed to reload routes", "error", err)
				continue
			}
			if ms, err := st.ListModels(); err != nil {
				logger.Error("failed to reload models", "error", err)
			} else {
				catalog.Swap(ms)
			}
			router.Reload(u, g, rt, encryptionKey)
		}
	}()

	// Periodically expire paid subscriptions and fall back to free plan.
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if n, err := st.ExpireAndFallbackToFree(); err != nil {
				logger.Error("subscription expiry failed", "error", err)
			} else if n > 0 {
				logger.Info("expired subscriptions with free fallback", "count", n)
			}
		}
	}()

	// Initialize rate limiter.
	rateLimiter := ratelimit.NewCompositeRateLimiter(st, logger)

	executor := proxy.NewExecutor(router, st, coll, rateLimiter, catalog, logger, cfg.Server.MaxRequestBody)
	proxyHandler := proxy.NewHandler(executor, router, st, coll, catalog, logger, cfg.Server.MaxRequestBody)

	// --- Proxy server ---
	proxyRouter := chi.NewRouter()
	proxyRouter.Use(middleware.Recoverer)
	proxyRouter.Use(middleware.RealIP)

	proxyRouter.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	proxyRouter.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := st.Ping(r.Context()); err != nil {
			http.Error(w, "database not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Create Hydra client for token introspection on the proxy if configured.
	var proxyIntrospector proxy.TokenIntrospector
	if cfg.Auth.OAuth.Hydra.AdminURL != "" {
		proxyIntrospector = &hydraIntrospectorAdapter{
			client: admin.NewHydraClient(cfg.Auth.OAuth.Hydra.AdminURL),
		}
		logger.Info("hydra token introspection enabled for proxy", "admin_url", cfg.Auth.OAuth.Hydra.AdminURL)
	}

	// Mount proxy routes.
	proxy.MountRoutes(proxyRouter, st, proxyHandler, cfg.Trace, rateLimiter, encryptionKey, logger, proxyIntrospector)

	proxyServer := &http.Server{
		Addr:    cfg.Server.ProxyAddr,
		Handler: proxyRouter,
	}

	// --- Admin server ---
	adminRouter := chi.NewRouter()
	adminRouter.Use(middleware.Recoverer)
	adminRouter.Use(middleware.RealIP)
	if len(cfg.CORS.AllowedOrigins) > 0 {
		adminRouter.Use(cors.Handler(cors.Options{
			AllowedOrigins:   cfg.CORS.AllowedOrigins,
			AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
			AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
			ExposedHeaders:   []string{"X-RateLimit-Limit", "X-RateLimit-Used", "X-RateLimit-Reset", "Retry-After"},
			AllowCredentials: true,
			MaxAge:           300,
		}))
	}

	adminRouter.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	adminRouter.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := st.Ping(r.Context()); err != nil {
			http.Error(w, "database not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Initialize JWT manager.
	jwtMgr := auth.NewJWTManager(cfg.Auth.JWTSecret, cfg.Auth.AccessTokenTTL, cfg.Auth.RefreshTokenTTL)

	// Mount admin API routes.
	admin.MountRoutes(adminRouter, st, cfg, encryptionKey, jwtMgr, catalog)

	adminServer := &http.Server{
		Addr:    cfg.Server.AdminAddr,
		Handler: adminRouter,
	}

	// Graceful shutdown.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		logger.Info("received signal, shutting down", "signal", sig.String())
		router.HealthChecker().Stop()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		proxyServer.Shutdown(ctx)
		adminServer.Shutdown(ctx)
	}()

	// Start both servers.
	go func() {
		logger.Info("starting admin API", "addr", cfg.Server.AdminAddr)
		if err := adminServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("admin server error: %v", err)
		}
	}()

	logger.Info("starting proxy", "addr", cfg.Server.ProxyAddr)
	if err := proxyServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("proxy server error: %v", err)
	}
}
