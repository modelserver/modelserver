package server

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/modelserver/modelserver/services/payserver/internal/gateway"
	"github.com/modelserver/modelserver/services/payserver/internal/notify"
	"github.com/modelserver/modelserver/services/payserver/internal/store"
)

type Config struct {
	APIKey       string
	Store        *store.Store
	Gateways     map[string]gateway.Gateway
	WeChatNotify *notify.WeChatNotifyHandler
	AlipayNotify *notify.AlipayNotifyHandler
	Logger       *slog.Logger
}

func NewRouter(cfg Config) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Authenticated endpoint for modelserver
	r.Group(func(r chi.Router) {
		r.Use(bearerAuthMiddleware(cfg.APIKey))
		r.Post("/payments", handleCreatePayment(cfg.Store, cfg.Gateways, cfg.Logger))
	})

	// Payment platform callbacks (no bearer auth, platform-native verification)
	r.Route("/notify", func(r chi.Router) {
		if cfg.WeChatNotify != nil {
			r.Post("/wechat", cfg.WeChatNotify.ServeHTTP)
		}
		if cfg.AlipayNotify != nil {
			r.Post("/alipay", cfg.AlipayNotify.ServeHTTP)
		}
	})

	return r
}
