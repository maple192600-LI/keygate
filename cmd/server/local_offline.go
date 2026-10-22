package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/tabloy/keygate/internal/branding"
	"github.com/tabloy/keygate/internal/config"
	"github.com/tabloy/keygate/internal/crypto"
	"github.com/tabloy/keygate/internal/handler"
	"github.com/tabloy/keygate/internal/middleware"
	"github.com/tabloy/keygate/internal/offline"
	"github.com/tabloy/keygate/internal/service"
	"github.com/tabloy/keygate/internal/store"
	"github.com/tabloy/keygate/pkg/response"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// The local runtime branches before constructing any online token service,
// payment integration, analytics collection, email queue or scheduled expiry.
func newLocalOfflineRouter(cfg *config.Config, db *store.Store, shutdown ...func()) *gin.Engine {
	master, err := hex.DecodeString(cfg.ReleaseKeyEncryptionKey)
	if err != nil || len(master) != 32 {
		panic("本地运行数据加密秘密无效")
	}
	db.LicenseKeyAEAD = crypto.MustDeriveAEAD(master, "license-key")
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.RedirectTrailingSlash = false
	_ = r.SetTrustedProxies(nil)
	r.Use(redactedRecovery(), middleware.RequestID())
	r.Use(func(c *gin.Context) {
		c.Header(branding.HeaderKey, branding.Project)
		c.Header("X-Frame-Options", "DENY")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "no-referrer")
		if c.Request.Host != "127.0.0.1:"+cfg.Port {
			response.Err(c, 403, "FORBIDDEN", "请求地址不属于本机管理端")
			c.Abort()
			return
		}
		origin := c.GetHeader("Origin")
		if (origin != "" && origin != cfg.BaseURL) || c.GetHeader("Sec-Fetch-Site") == "cross-site" {
			response.Err(c, 403, "FORBIDDEN", "请从本机管理窗口操作")
			c.Abort()
			return
		}
		c.Next()
	})
	r.GET("/health", func(c *gin.Context) {
		if err := db.DB.PingContext(c); err != nil {
			response.Err(c, 503, "DATABASE_UNAVAILABLE", "本地数据库暂时不可用")
			return
		}
		value := gin.H{"status": "ok", "deployment": "local_offline"}
		if challenge, err := hex.DecodeString(c.Query("challenge")); err == nil && len(challenge) == 32 {
			if key, err := hex.DecodeString(cfg.SnailRuntimeToken); err == nil && len(key) == 32 {
				mac := hmac.New(sha256.New, key)
				mac.Write([]byte("snail-runtime-ready/v1\x00"))
				mac.Write(challenge)
				value["proof"] = hex.EncodeToString(mac.Sum(nil))
			}
		}
		response.OK(c, value)
	})
	if token := cfg.SnailRuntimeToken; len(token) == 64 && len(shutdown) == 1 {
		r.POST("/internal/shutdown", func(c *gin.Context) {
			if subtle.ConstantTimeCompare([]byte(c.GetHeader("X-Snail-Runtime-Token")), []byte(token)) != 1 {
				response.Unauthorized(c, "此操作仅供本机管理启动器使用")
				return
			}
			response.OK(c, gin.H{"stopping": true})
			shutdown[0]()
		})
	}
	api := r.Group("/api/v1")
	setup := handler.NewSetupHandler(db, nil)
	local := handler.NewLocalAuthHandler(db, cfg.JWTSecret)
	keys := offline.NewKeyStore(cfg.SnailDataDir)
	local.OnSessionEnd = keys.Lock
	api.GET("/setup/status", setup.Status)
	api.GET("/config", func(c *gin.Context) {
		response.OK(c, gin.H{"site_name": "蜗牛客户与授权中心", "deployment": "local_offline", "attribution_text": branding.Tagline, "attribution_url": branding.URL})
	})
	authRate := middleware.RateLimitByIPScoped("local-password:"+cfg.Port, 10, time.Minute)
	api.POST("/local/setup", func(c *gin.Context) {
		// Loopback and Origin are not a Windows-user identity. Only the
		// launching Electron main process holds this ephemeral capability.
		if len(cfg.SnailRuntimeToken) != 64 || subtle.ConstantTimeCompare([]byte(c.GetHeader("X-Snail-Setup-Token")), []byte(cfg.SnailRuntimeToken)) != 1 {
			response.Err(c, 403, "LOCAL_WINDOW_REQUIRED", "请从本机独立管理窗口完成首次设置")
			c.Abort()
			return
		}
		c.Next()
	}, authRate, local.Setup)
	api.POST("/auth/password/login", authRate, local.Login)
	api.POST("/auth/refresh", middleware.RateLimitByIPScoped("local-refresh", 60, time.Minute), local.Refresh)
	owner := api.Group("", local.RequireOwner())
	owner.GET("/auth/me", local.Me)
	owner.POST("/auth/logout", local.Logout)
	owner.POST("/auth/password/change", authRate, local.ChangePassword)
	customers := &handler.CommercialCustomerHandler{Store: db}
	owner.GET("/admin/commercial-customers", customers.List)
	owner.GET("/admin/commercial-customers/:id", customers.Get)
	owner.POST("/admin/commercial-customers", customers.Save)
	owner.PATCH("/admin/commercial-customers/:id", customers.Save)
	requests := &handler.OfflineRequestHandler{Store: db}
	owner.POST("/admin/offline-requests", requests.Import)
	keyHandler := &handler.OfflineKeyHandler{Store: db, Keys: keys}
	owner.GET("/admin/offline-key/public", keyHandler.Public)
	owner.POST("/admin/offline-key/create", authRate, keyHandler.Create)
	owner.POST("/admin/offline-key/unlock", authRate, keyHandler.Unlock)
	owner.POST("/admin/offline-key/lock", keyHandler.Lock)
	owner.GET("/admin/offline-key/encrypted", keyHandler.ExportEncrypted)
	owner.POST("/admin/offline-key/import/preview", authRate, keyHandler.ImportPreview)
	owner.POST("/admin/offline-key/import", authRate, keyHandler.Import)
	issuance := &handler.OfflineLicenseHandler{Service: &service.OfflineIssuanceService{Store: db, Keys: keys, PreviewSecret: []byte(cfg.JWTSecret)}}
	owner.POST("/admin/offline-licenses/preview", issuance.Preview)
	owner.POST("/admin/offline-licenses", issuance.Issue("initial"))
	owner.POST("/admin/offline-licenses/:id/renew", issuance.Issue("renew"))
	owner.POST("/admin/offline-licenses/:id/transfer", issuance.Issue("transfer"))
	owner.GET("/admin/license-issuances/:id/file", issuance.File)
	owner.GET("/admin/issuance-operations/:operationId", issuance.Operation)
	deliveries := &handler.OfflineDeliveryHandler{Store: db}
	owner.GET("/admin/license-issuances/:id/deliveries", deliveries.List)
	owner.POST("/admin/license-issuances/:id/deliveries", deliveries.Record)
	lifecycle := &handler.OfflineLifecycleHandler{Store: db}
	owner.GET("/admin/overview", lifecycle.Overview)
	owner.GET("/admin/offline-licenses", lifecycle.List)
	owner.GET("/admin/offline-licenses/:id", lifecycle.Get)
	owner.POST("/admin/offline-licenses/:id/issuance-state", lifecycle.State("license"))
	owner.POST("/admin/commercial-customers/:id/issuance-state", lifecycle.State("customer"))
	serveFrontend(r)
	r.NoRoute(func(c *gin.Context) { response.Err(c, 404, "NOT_FOUND", "此本地功能入口不存在") })
	return r
}
func runLocalOffline(cfg *config.Config, db *store.Store, logger *slog.Logger) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	router := newLocalOfflineRouter(cfg, db, cancel)
	server := &http.Server{Addr: "127.0.0.1:" + cfg.Port, Handler: router, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	logger.Info("蜗牛客户与授权中心已启动", "address", cfg.BaseURL, "deployment", "local_offline")
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdown, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		return server.Shutdown(shutdown)
	}
	return nil
}
