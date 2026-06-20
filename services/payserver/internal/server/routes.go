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
	AdminDistFS  fs.FS     // nil = no admin frontend embedded
	Logger       *slog.Logger
}

// adminSecurityHeaders sets a baseline set of security response headers on
// all /admin/* responses (both JSON and SPA HTML). The CSP allows
// 'unsafe-inline' for style because Vite inlines critical CSS — revisit
// once the build emits an external style sheet with a nonce.
func adminSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
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

	// Admin SPA is gated on OIDC being configured; without auth, /admin/* 404s.
	if cfg.OIDCAuth != nil {
		// serveSPA streams index.html. Guards on AdminDistFS so tests (and
		// boots without an embedded frontend) get a clean 404 rather than a
		// nil-deref. Pulls the dist FS at call time so the closure picks up
		// the value the caller passed in.
		serveSPA := func(w http.ResponseWriter, req *http.Request) {
			if cfg.AdminDistFS == nil {
				http.NotFound(w, req)
				return
			}
			f, err := cfg.AdminDistFS.Open("index.html")
			if err != nil {
				http.Error(w, "admin not built", http.StatusNotFound)
				return
			}
			defer f.Close()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.Copy(w, f)
		}

		r.Route("/admin", func(r chi.Router) {
			r.Use(adminSecurityHeaders)

			// Static assets and the SPA shell must be reachable without a
			// session — otherwise the browser can never load the bundle that
			// would redirect the user to /admin/login.
			if cfg.AdminDistFS != nil {
				fileServer := http.FileServerFS(cfg.AdminDistFS)
				r.Get("/assets/*", http.StripPrefix("/admin/", fileServer).ServeHTTP)
				r.Get("/vite.svg", http.StripPrefix("/admin/", fileServer).ServeHTTP)
			}

			r.Get("/login", cfg.OIDCAuth.LoginHandler)
			r.Get("/callback", cfg.OIDCAuth.CallbackHandler)
			r.Post("/logout", cfg.OIDCAuth.LogoutHandler)

			// Session-protected JSON endpoints.
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

			// SPA fallback: anything under /admin not matched above (e.g.
			// /admin, /admin/, /admin/tenants/<uuid>) serves index.html so
			// the React router can take over. NotFound runs only when chi
			// has no explicit match — so JSON endpoints above always win.
			r.NotFound(serveSPA)
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

	return r
}
