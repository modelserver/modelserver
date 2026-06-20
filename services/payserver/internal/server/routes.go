package server

import (
	"io"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/modelserver/modelserver/services/payserver/internal/gateway"
	"github.com/modelserver/modelserver/services/payserver/internal/notify"
	"github.com/modelserver/modelserver/services/payserver/internal/store"
)

type Config struct {
	Store        *store.Store
	Gateways     map[string]gateway.Gateway
	WeChatNotify *notify.WeChatNotifyHandler
	AlipayNotify *notify.AlipayNotifyHandler
	StripeNotify *notify.StripeNotifyHandler
	OIDCAuth     *OIDCAuth // nil = admin disabled
	AdminDistFS  fs.FS    // nil = no admin frontend embedded
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
		r.Use(tenantAuthMiddleware(cfg.Store, cfg.Logger))
		r.Post("/payments", handleCreatePayment(cfg.Store, cfg.Gateways, cfg.Logger))
	})

	// Admin panel: OIDC-protected routes (only when OIDCAuth is configured).
	if cfg.OIDCAuth != nil {
		r.Route("/admin", func(r chi.Router) {
			r.Get("/login", cfg.OIDCAuth.LoginHandler)
			r.Get("/callback", cfg.OIDCAuth.CallbackHandler)
			r.Post("/logout", cfg.OIDCAuth.LogoutHandler)
			// Everything else under /admin requires a session.
			r.Group(func(r chi.Router) {
				r.Use(cfg.OIDCAuth.RequireSession)
				r.Get("/whoami", cfg.OIDCAuth.WhoamiHandler)

				r.Get("/tenants", handleListTenants(cfg.Store))
				r.Post("/tenants", handleCreateTenant(cfg.Store))
				r.Get("/tenants/{id}", handleGetTenant(cfg.Store))
				r.Patch("/tenants/{id}", handleUpdateTenant(cfg.Store))
				r.Delete("/tenants/{id}", handleDeleteTenant(cfg.Store))
				r.Post("/tenants/{id}/rotate-secret", handleRotateTenantSecret(cfg.Store))

				r.Get("/payments", handleListPayments(cfg.Store))
				r.Get("/payments/{id}", handleGetPayment(cfg.Store))
			})
		})
	}

	// Payment platform callbacks (no bearer auth, platform-native verification)
	r.Route("/notify", func(r chi.Router) {
		if cfg.WeChatNotify != nil {
			r.Post("/wechat", cfg.WeChatNotify.ServeHTTP)
		}
		if cfg.AlipayNotify != nil {
			r.Post("/alipay", cfg.AlipayNotify.ServeHTTP)
		}
		if cfg.StripeNotify != nil {
			r.Post("/stripe", cfg.StripeNotify.ServeHTTP)
		}
	})

	// Admin SPA static assets (only when AdminDistFS is wired in).
	if cfg.AdminDistFS != nil {
		fileServer := http.FileServerFS(cfg.AdminDistFS)
		r.Get("/admin/assets/*", http.StripPrefix("/admin/", fileServer).ServeHTTP)
		r.Get("/admin/vite.svg", http.StripPrefix("/admin/", fileServer).ServeHTTP)

		serveSPA := func(w http.ResponseWriter, req *http.Request) {
			f, err := cfg.AdminDistFS.Open("index.html")
			if err != nil {
				http.Error(w, "admin not built", http.StatusNotFound)
				return
			}
			defer f.Close()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.Copy(w, f)
		}
		r.Get("/admin", serveSPA)
		r.Get("/admin/", serveSPA)
		r.Get("/admin/tenants", serveSPA)
		r.Get("/admin/tenants/*", serveSPA)
		r.Get("/admin/payments", serveSPA)
		r.Get("/admin/payments/*", serveSPA)
	}

	return r
}
