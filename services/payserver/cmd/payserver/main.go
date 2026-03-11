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

	"github.com/wechatpay-apiv3/wechatpay-go/core/downloader"
	"github.com/wechatpay-apiv3/wechatpay-go/utils"

	"github.com/modelserver/modelserver/services/payserver/internal/compensate"
	"github.com/modelserver/modelserver/services/payserver/internal/config"
	"github.com/modelserver/modelserver/services/payserver/internal/gateway"
	notifyPkg "github.com/modelserver/modelserver/services/payserver/internal/notify"
	"github.com/modelserver/modelserver/services/payserver/internal/server"
	"github.com/modelserver/modelserver/services/payserver/internal/store"
)

func main() {
	configPath := flag.String("config", "", "path to config file")
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

	if cfg.APIKey == "" {
		log.Fatal("api_key is required (api_key in config or PAYSERVER_API_KEY env)")
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

	// Database.
	if cfg.DB.URL == "" {
		log.Fatal("database URL is required (db.url or PAYSERVER_DB_URL)")
	}
	st, err := store.New(cfg.DB.URL, logger)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer st.Close()
	logger.Info("connected to database")

	// Callback client.
	callbackClient := notifyPkg.NewCallbackClient(
		cfg.Callback.ModelserverURL,
		cfg.Callback.WebhookSecret,
		cfg.Callback.Timeout,
	)

	// Initialize gateways.
	gateways := make(map[string]gateway.Gateway)
	var wechatNotify *notifyPkg.WeChatNotifyHandler
	var alipayNotify *notifyPkg.AlipayNotifyHandler

	ctx := context.Background()

	// WeChat gateway.
	if cfg.WeChat.AppID != "" && cfg.WeChat.MchID != "" {
		wg, err := gateway.NewWeChatGateway(ctx, gateway.WeChatGatewayConfig{
			AppID:             cfg.WeChat.AppID,
			MchID:             cfg.WeChat.MchID,
			MchAPIv3Key:       cfg.WeChat.MchAPIv3Key,
			MchSerialNo:       cfg.WeChat.MchSerialNo,
			MchPrivateKeyPath: cfg.WeChat.MchPrivateKeyPath,
			NotifyURL:         cfg.WeChat.NotifyURL,
		})
		if err != nil {
			log.Fatalf("failed to init wechat gateway: %v", err)
		}
		gateways["wechat"] = wg

		// WeChat notify handler needs the SDK's certificate downloader for signature verification
		privKey, err := utils.LoadPrivateKeyWithPath(cfg.WeChat.MchPrivateKeyPath)
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
			AlipayPublicKeyPath: cfg.Alipay.AlipayPublicKeyPath,
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

	if len(gateways) == 0 {
		logger.Warn("no payment gateways configured")
	}

	// Compensation worker.
	compWorker := compensate.NewWorker(st, callbackClient, logger)
	compWorker.Start()
	defer compWorker.Stop()

	// HTTP server.
	router := server.NewRouter(server.Config{
		APIKey:       cfg.APIKey,
		Store:        st,
		Gateways:     gateways,
		WeChatNotify: wechatNotify,
		AlipayNotify: alipayNotify,
		Logger:       logger,
	})

	srv := &http.Server{
		Addr:    cfg.Server.Addr,
		Handler: router,
	}

	// Graceful shutdown.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		logger.Info("received signal, shutting down", "signal", sig.String())
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	logger.Info("starting payserver", "addr", cfg.Server.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
