package main

import (
	"context"
	"crypto/rsa"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wechatpay-apiv3/wechatpay-go/core/downloader"
	"github.com/wechatpay-apiv3/wechatpay-go/utils"

	"github.com/modelserver/modelserver/services/payserver/internal/compensate"
	"github.com/modelserver/modelserver/services/payserver/internal/config"
	"github.com/modelserver/modelserver/services/payserver/internal/gateway"
	notifyPkg "github.com/modelserver/modelserver/services/payserver/internal/notify"
	"github.com/modelserver/modelserver/services/payserver/internal/server"
	"github.com/modelserver/modelserver/services/payserver/internal/store"
	"github.com/modelserver/modelserver/services/payserver/internal/tenant"
)

//go:embed admin_dist
var adminDistFS embed.FS

func main() {
	// Subcommand dispatcher: `payserver admin rescue --email <addr>`
	if len(os.Args) >= 3 && os.Args[1] == "admin" && os.Args[2] == "rescue" {
		runRescue(os.Args[3:])
		return
	}
	runServer()
}

// rescueMaxTTL caps operator-supplied --ttl. Mirrors the 24h ceiling the
// OIDC CallbackHandler uses for normal session cookies — there is no
// legitimate reason a rescue session needs to outlive a normal one.
const rescueMaxTTL = 24 * time.Hour

// rescueMinSessionSecretChars matches the value enforced by NewOIDCAuth
// (kept as a duplicate constant to avoid making cmd/payserver depend on
// the server package's internal constant). If either value changes, change
// both.
const rescueMinSessionSecretChars = 32

func runRescue(args []string) {
	fs := flag.NewFlagSet("rescue", flag.ExitOnError)
	email := fs.String("email", "", "operator email to encode into the session")
	ttl := fs.Duration("ttl", time.Hour, "session lifetime (max 24h)")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *email == "" {
		fmt.Fprintln(os.Stderr, "rescue: --email is required")
		os.Exit(2)
	}
	if *ttl <= 0 || *ttl > rescueMaxTTL {
		fmt.Fprintf(os.Stderr, "rescue: --ttl must be >0 and <=%s\n", rescueMaxTTL)
		os.Exit(2)
	}
	secret := os.Getenv("PAYSERVER_OIDC_SESSION_SECRET")
	if secret == "" {
		fmt.Fprintln(os.Stderr, "rescue: PAYSERVER_OIDC_SESSION_SECRET is required")
		os.Exit(2)
	}
	if len(secret) < rescueMinSessionSecretChars {
		fmt.Fprintf(os.Stderr, "rescue: PAYSERVER_OIDC_SESSION_SECRET must be at least %d characters\n", rescueMinSessionSecretChars)
		os.Exit(2)
	}
	// NOTE: rescue intentionally does NOT enforce the OIDC AllowedEmails
	// allowlist. The rescue path is the escape hatch for when OIDC config
	// (including the allowlist itself) is broken or locks out everyone.
	// Restricting it to allowed_emails would defeat its purpose. Access
	// control here is via host-level access to PAYSERVER_OIDC_SESSION_SECRET
	// (typically SSH/kubectl exec required); the audit line below is the
	// only on-host record of the operation.
	sess := server.AdminSession{
		Email:     *email,
		Name:      *email,
		ExpiresAt: time.Now().Add(*ttl),
	}
	token, err := server.EncodeSession(sess, []byte(secret))
	if err != nil {
		fmt.Fprintf(os.Stderr, "rescue: encode session: %v\n", err)
		os.Exit(1)
	}
	// Structured audit line on stdout so container log collectors capture
	// it (operators frequently scrape only stdout). Initialize a minimal
	// JSON slog handler here rather than reusing runServer's logger —
	// rescue is the escape-hatch path and must work without any of the
	// runServer setup having run.
	auditLogger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	auditLogger.Info("admin rescue session issued",
		"email", *email, "ttl", ttl.String(), "pid", os.Getpid())

	// Operator-facing instructions + the token go to stderr only. Stdout
	// is reserved for the structured audit record above so container log
	// collectors (typically stdout-only) capture the audit trail without
	// also capturing the bearer-equivalent session token. Scripts that
	// need to scrape the token should redirect stderr (`2>&1` or capture
	// it explicitly).
	fmt.Fprintf(os.Stderr, "issued rescue session for=%s ttl=%s\n", *email, *ttl)
	fmt.Fprintln(os.Stderr, "set this cookie on your /admin/* domain to bypass OIDC:")
	fmt.Fprintf(os.Stderr, "  payserver_admin_session=%s\n", token)
}

func runServer() {
	configPath := flag.String("config", "", "path to config file")
	flag.Parse()

	// Auto-discover config.yml in the working directory if --config not given.
	path := *configPath
	if path == "" {
		if _, statErr := os.Stat("config.yml"); statErr == nil {
			path = "config.yml"
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid config: %v", err)
	}

	// Logger.
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
	logger := slog.New(handler).With("component", "payserver")

	// Deprecation warning for old APIKey field.
	if cfg.APIKey != "" {
		logger.Warn("PAYSERVER_API_KEY / cfg.api_key is deprecated and ignored; manage credentials per-tenant via the admin UI")
	}

	// Database. (db.url required check lives in cfg.Validate, called above.)
	// Bootstrap values for migration 002 (consumed only on first apply).
	// On subsequent boots, 002 already exists in schema_migrations so the
	// runner skips the SQL and these values go unread.
	var bootstrap store.MigrationBootstrap
	if defaultSecret := os.Getenv("PAYSERVER_DEFAULT_TENANT_SECRET"); defaultSecret != "" {
		hash, err := tenant.HashSecret(defaultSecret)
		if err != nil {
			log.Fatalf("hash default tenant secret: %v", err)
		}
		bootstrap = store.MigrationBootstrap{
			DefaultTenantSecretHash: hash,
			DefaultCallbackURL:      cfg.Callback.ModelserverURL,
			DefaultCallbackSecret:   cfg.Callback.WebhookSecret,
		}
	}

	st, err := store.New(cfg.DB.URL, logger, bootstrap)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer st.Close()
	logger.Info("connected to database")

	// Callback client.
	// TODO(Task 9): drop cfg.Callback.ModelserverURL + cfg.Callback.WebhookSecret (now per-tenant).
	callbackClient := notifyPkg.NewCallbackClientWithOpts(cfg.Callback.Timeout, cfg.Callback.AllowPrivateNetworks)

	// Initialize gateways.
	gateways := make(map[string]gateway.Gateway)
	var wechatNotify *notifyPkg.WeChatNotifyHandler
	var alipayNotify *notifyPkg.AlipayNotifyHandler
	var stripeNotify *notifyPkg.StripeNotifyHandler

	ctx := context.Background()

	// WeChat gateway.
	if cfg.WeChat.AppID != "" && cfg.WeChat.MchID != "" {
		wg, err := gateway.NewWeChatGateway(ctx, gateway.WeChatGatewayConfig{
			AppID:             cfg.WeChat.AppID,
			MchID:             cfg.WeChat.MchID,
			MchAPIv3Key:       cfg.WeChat.MchAPIv3Key,
			MchSerialNo:       cfg.WeChat.MchSerialNo,
			MchPrivateKeyPath: cfg.WeChat.MchPrivateKeyPath,
			MchPrivateKeyPEM:  cfg.WeChat.MchPrivateKeyPEM,
			NotifyURL:         cfg.WeChat.NotifyURL,
		})
		if err != nil {
			log.Fatalf("failed to init wechat gateway: %v", err)
		}
		gateways["wechat"] = wg

		// WeChat notify handler needs the SDK's certificate downloader for signature verification
		var privKey *rsa.PrivateKey
		if cfg.WeChat.MchPrivateKeyPEM != "" {
			privKey, err = utils.LoadPrivateKey(cfg.WeChat.MchPrivateKeyPEM)
		} else {
			privKey, err = utils.LoadPrivateKeyWithPath(cfg.WeChat.MchPrivateKeyPath)
		}
		if err != nil {
			log.Fatalf("failed to load wechat private key for notify: %v", err)
		}
		mgr := downloader.NewCertificateDownloaderMgr(ctx)
		if err := mgr.RegisterDownloaderWithPrivateKey(ctx, privKey, cfg.WeChat.MchSerialNo, cfg.WeChat.MchID, cfg.WeChat.MchAPIv3Key); err != nil {
			log.Fatalf("failed to register wechat cert downloader: %v", err)
		}
		certVisitor := mgr.GetCertificateVisitor(cfg.WeChat.MchID)
		notifyHandler := notifyPkg.NewWeChatNotifyHandlerFromVerifier(cfg.WeChat.MchAPIv3Key, certVisitor, st, callbackClient, logger)
		wechatNotify = notifyHandler
		logger.Info("wechat gateway initialized")
	}

	// Alipay gateway.
	if cfg.Alipay.AppID != "" {
		ag, err := gateway.NewAlipayGateway(gateway.AlipayGatewayConfig{
			AppID:               cfg.Alipay.AppID,
			PrivateKeyPath:      cfg.Alipay.PrivateKeyPath,
			PrivateKeyPEM:       cfg.Alipay.PrivateKeyPEM,
			PublicKeyPath:       cfg.Alipay.PublicKeyPath,
			PublicKeyPEM:        cfg.Alipay.PublicKeyPEM,
			NotifyURL:           cfg.Alipay.NotifyURL,
			ReturnURL:           cfg.Alipay.ReturnURL,
		})
		if err != nil {
			log.Fatalf("failed to init alipay gateway: %v", err)
		}
		gateways["alipay"] = ag
		alipayNotify = notifyPkg.NewAlipayNotifyHandler(ag, st, callbackClient, logger)
		logger.Info("alipay gateway initialized")
	}

	// Stripe gateway. (webhook_secret presence enforced by cfg.Validate.)
	if cfg.Stripe.SecretKey != "" {
		sg, err := gateway.NewStripeGateway(gateway.StripeGatewayConfig{
			SecretKey:     cfg.Stripe.SecretKey,
			SuccessURL:    cfg.Stripe.SuccessURL,
			CancelURL:     cfg.Stripe.CancelURL,
			DefaultLocale: cfg.Stripe.DefaultLocale,
		})
		if err != nil {
			log.Fatalf("failed to init stripe gateway: %v", err)
		}
		gateways["stripe"] = sg
		stripeNotify = notifyPkg.NewStripeNotifyHandler(cfg.Stripe.WebhookSecret, st, callbackClient, logger)
		logger.Info("stripe gateway initialized")
	}

	if len(gateways) == 0 {
		logger.Warn("no payment gateways configured")
	}

	// Compensation worker. Stop ordering (handled below in the signal
	// goroutine): worker first, then HTTP server. Stopping the worker
	// first guarantees no in-flight pgx UPDATE races against pool-close,
	// and ensures the worker's tick-driven callbacks don't fire mid-
	// shutdown when downstream tenants may already see new request
	// rejections.
	compWorker := compensate.NewWorker(st, callbackClient, logger)
	compWorker.Start()

	// OIDC admin auth (optional).
	var oidcAuth *server.OIDCAuth
	if cfg.OIDC.IssuerURL != "" {
		oidcAuth, err = server.NewOIDCAuth(ctx, cfg.OIDC, logger)
		if err != nil {
			log.Fatalf("oidc init: %v", err)
		}
		logger.Info("oidc enabled", "issuer", cfg.OIDC.IssuerURL)
	}

	// Admin frontend embed.
	adminSubFS, err := fs.Sub(adminDistFS, "admin_dist")
	if err != nil {
		log.Fatalf("admin sub-fs: %v", err)
	}

	// HTTP server.
	router := server.NewRouter(server.Config{
		Store:        st,
		Gateways:     gateways,
		WeChatNotify: wechatNotify,
		AlipayNotify: alipayNotify,
		StripeNotify: stripeNotify,
		OIDCAuth:     oidcAuth,
		AdminDistFS:  adminSubFS,
		Logger:       logger,
	})

	srv := &http.Server{
		Addr:    cfg.Server.Addr,
		Handler: router,
	}

	// Graceful shutdown. Stop the compensate worker BEFORE the HTTP
	// server so background writes finish on a still-open DB pool. Then
	// drain HTTP requests so in-flight gateway callbacks complete.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		logger.Info("received signal, shutting down", "signal", sig.String())
		compWorker.Stop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	logger.Info("starting payserver", "addr", cfg.Server.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
