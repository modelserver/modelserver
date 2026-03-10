package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/store"
)

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
		cfg, err = config.Load(strings.NewReader(""))
	}
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	cfg.ApplyEnvOverrides()

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
	logger.Info("connected to database")

	// --- Proxy server ---
	proxyRouter := chi.NewRouter()
	proxyRouter.Use(middleware.Recoverer)
	proxyRouter.Use(middleware.RealIP)

	proxyRouter.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	proxyRouter.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := st.DB().Ping(); err != nil {
			http.Error(w, "database not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// TODO: Mount proxy routes in Phase 2

	proxyServer := &http.Server{
		Addr:    cfg.Server.ProxyAddr,
		Handler: proxyRouter,
	}

	// --- Admin server ---
	adminRouter := chi.NewRouter()
	adminRouter.Use(middleware.Recoverer)
	adminRouter.Use(middleware.RealIP)

	adminRouter.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	adminRouter.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := st.DB().Ping(); err != nil {
			http.Error(w, "database not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// TODO: Mount admin routes in Phase 4

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
