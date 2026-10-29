package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tabloy/keygate/internal/branding"
	"github.com/tabloy/keygate/internal/config"
	"github.com/tabloy/keygate/internal/crypto"
	"github.com/tabloy/keygate/internal/handler"
	"github.com/tabloy/keygate/internal/middleware"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/offline"
	"github.com/tabloy/keygate/internal/service"
	"github.com/tabloy/keygate/internal/store"
	"github.com/tabloy/keygate/pkg/response"
	"github.com/uptrace/bun"
)

// The local runtime branches before constructing any online token service,
// payment integration, analytics collection, email queue or scheduled expiry.
func newLocalOfflineRouter(cfg *config.Config, db *store.Store, shutdown ...func()) *gin.Engine {
	router, _, _ := newManagedLocalRouter(cfg, db, nil, shutdown...)
	return router
}
func newManagedLocalRouter(cfg *config.Config, db *store.Store, activate func(*service.RestoreCandidate) error, shutdown ...func()) (*gin.Engine, *gin.Engine, func(context.Context) error) {
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
	gate := handler.NewLocalWriteGate()
	api := r.Group("/api/v1", gate.Middleware())
	setup := handler.NewSetupHandler(db, nil)
	local := handler.NewLocalAuthHandler(db, cfg.JWTSecret)
	identityDir := cfg.SnailIdentityDir
	if identityDir == "" {
		identityDir = cfg.SnailDataDir
	}
	keys := offline.NewKeyStore(identityDir)
	servicePassphrase, err := initializeOfflineServiceIdentity(context.Background(), db, keys, master)
	if err != nil {
		panic(err)
	}
	api.GET("/setup/status", setup.Status)
	api.GET("/config", func(c *gin.Context) {
		response.OK(c, gin.H{"site_name": "小云雀客户授权中心", "deployment": "local_offline", "attribution_text": branding.Tagline, "attribution_url": branding.URL})
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
	api.POST("/local/recovery/reset", middleware.RateLimitByIPScoped("local-owner-recovery", 5, 15*time.Minute), local.ResetRecovery)
	owner := api.Group("", local.RequireOwner())
	owner.GET("/auth/me", local.Me)
	owner.POST("/auth/logout", local.Logout)
	owner.POST("/auth/password/change", authRate, local.ChangePassword)
	owner.GET("/admin/local-recovery/status", local.RecoveryStatus)
	owner.POST("/admin/local-recovery/rotate", authRate, local.RotateRecovery)
	customers := &handler.CommercialCustomerHandler{Store: db}
	owner.GET("/admin/commercial-customers", customers.List)
	owner.GET("/admin/commercial-customers/:id", customers.Get)
	owner.POST("/admin/commercial-customers", customers.Save)
	owner.PATCH("/admin/commercial-customers/:id", customers.Save)
	requests := &handler.OfflineRequestHandler{Store: db}
	owner.POST("/admin/offline-requests", requests.Import)
	keyHandler := &handler.OfflineKeyHandler{Store: db, Keys: keys, ServicePassphrase: servicePassphrase}
	owner.GET("/admin/offline-key/public", keyHandler.Public)
	owner.POST("/admin/offline-key/create", authRate, keyHandler.Create)
	owner.POST("/admin/offline-key/unlock", authRate, keyHandler.Unlock)
	owner.POST("/admin/offline-key/service-enable", authRate, keyHandler.EnableService)
	owner.GET("/admin/offline-key/encrypted", keyHandler.ExportEncrypted)
	owner.POST("/admin/offline-key/import/preview", authRate, keyHandler.ImportPreview)
	owner.POST("/admin/offline-key/import", authRate, keyHandler.Import)
	owner.POST("/admin/offline-key/recovery", authRate, keyHandler.Recovery)
	issuance := &handler.OfflineLicenseHandler{Service: &service.OfflineIssuanceService{Store: db, Keys: keys, PreviewSecret: []byte(cfg.JWTSecret), ServicePassphrase: servicePassphrase}}
	owner.POST("/admin/offline-licenses/preview", issuance.Preview)
	owner.POST("/admin/offline-licenses", issuance.Issue("initial"))
	owner.POST("/admin/offline-licenses/:id/renew", issuance.Issue("renew"))
	owner.POST("/admin/offline-licenses/:id/transfer", issuance.Issue("transfer"))
	owner.GET("/admin/license-issuances/:id/file", issuance.File)
	owner.GET("/admin/issuance-operations/:operationId", issuance.Operation)
	deliveries := &handler.OfflineDeliveryHandler{Store: db}
	owner.GET("/admin/license-issuances/:id/deliveries", deliveries.List)
	owner.POST("/admin/license-issuances/:id/deliveries", deliveries.Record)
	owner.GET("/admin/offline-licenses/:id/deliveries", deliveries.ListLicense)
	owner.POST("/admin/offline-licenses/:id/deliveries", deliveries.RecordLicense)
	lifecycle := &handler.OfflineLifecycleHandler{Store: db}
	owner.GET("/admin/overview", lifecycle.Overview)
	owner.GET("/admin/offline-licenses", lifecycle.List)
	owner.GET("/admin/offline-licenses/:id", lifecycle.Get)
	owner.GET("/admin/offline-licenses/:id/code", issuance.Code)
	owner.POST("/admin/offline-licenses/:id/issuance-state", lifecycle.State("license"))
	owner.POST("/admin/commercial-customers/:id/issuance-state", lifecycle.State("customer"))
	backups := &handler.OfflineBackupHandler{Service: &service.OfflineBackupService{Store: db, Keys: keys, DatabaseURL: cfg.DatabaseURL, DataDir: cfg.SnailDataDir, MigrationDir: "db/migrations", EncryptionSecret: cfg.ReleaseKeyEncryptionKey}, Gate: gate, Activate: activate}
	owner.POST("/admin/business-bundle/export", backups.ExportBusiness)
	owner.POST("/admin/business-bundle/preview", backups.PreviewBusiness)
	owner.POST("/admin/business-bundle/import", backups.ConfirmBusiness)
	owner.POST("/admin/backups", backups.Backup)
	owner.POST("/admin/backups/preview", backups.PreviewRestore)
	owner.POST("/admin/backups/restore", backups.ConfirmRestore)
	owner.POST("/admin/data-previews/cancel", backups.Cancel)
	owner.GET("/admin/local-settings", backups.Settings)
	owner.PATCH("/admin/local-settings", backups.Settings)
	owner.GET("/admin/local-operations", backups.Operations)
	serveFrontend(r)
	r.NoRoute(func(c *gin.Context) { response.Err(c, 404, "NOT_FOUND", "此本地功能入口不存在") })
	client := gin.New()
	client.RedirectTrailingSlash = false
	_ = client.SetTrustedProxies(nil)
	client.Use(redactedRecovery(), middleware.RequestID(), func(c *gin.Context) {
		c.Header(branding.HeaderKey, branding.Project)
		c.Header("X-Frame-Options", "DENY")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "no-referrer")
		c.Next()
	})
	publicAPI := client.Group("/api/v1/client", gate.Middleware())
	clientHandler := &handler.ClientActivationHandler{Service: &service.ClientActivationService{Store: db, Keys: keys}}
	publicAPI.POST("/activate", middleware.RateLimitByIPScoped("snail-client-activate", 30, time.Minute), clientHandler.Activate)
	publicAPI.POST("/sync", middleware.RateLimitByIPScoped("snail-client-sync", 60, time.Minute), clientHandler.Sync)
	client.NoRoute(func(c *gin.Context) { response.Err(c, 404, "NOT_FOUND", "客户端服务入口不存在") })
	return r, client, func(ctx context.Context) error { keys.Lock(); return backups.Close(ctx) }
}
func runLocalOffline(cfg *config.Config, db *store.Store, logger *slog.Logger) (resultErr error) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	managed := newLocalRouterSwitch(cfg, db, cancel)
	defer func() { resultErr = errors.Join(resultErr, managed.Close()) }()
	clientPort := cfg.SnailClientPort
	if clientPort == "" {
		clientPort = "9100"
	}
	if clientPort == cfg.Port {
		return fmt.Errorf("SNAIL_CLIENT_PORT must differ from the management port")
	}
	server := &http.Server{Addr: "127.0.0.1:" + cfg.Port, Handler: managed, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 2 * time.Minute, WriteTimeout: 2 * time.Minute, IdleTimeout: 60 * time.Second}
	clientServer := &http.Server{Addr: "127.0.0.1:" + clientPort, Handler: managed.ClientHandler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 2 * time.Minute, WriteTimeout: 2 * time.Minute, IdleTimeout: 60 * time.Second}
	done := make(chan error, 2)
	go func() { done <- server.ListenAndServe() }()
	go func() { done <- clientServer.ListenAndServe() }()
	logger.Info("小云雀客户授权中心已启动", "management_address", cfg.BaseURL, "client_address", "http://127.0.0.1:"+clientPort, "deployment", "local_offline")
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			_ = server.Close()
			_ = clientServer.Close()
			return err
		}
	case <-ctx.Done():
		shutdown, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		return errors.Join(server.Shutdown(shutdown), clientServer.Shutdown(shutdown))
	}
	return nil
}

func initializeOfflineServiceIdentity(ctx context.Context, db *store.Store, keys *offline.KeyStore, master []byte) (string, error) {
	passphrase, err := offline.ServicePassphrase(master)
	if err != nil {
		return "", err
	}
	physical, err := keys.Status()
	if err != nil {
		return "", err
	}
	expected, err := db.ExpectedSigningIdentity(ctx, physical)
	if err != nil {
		return "", err
	}
	if physical.State == "uncreated" {
		return passphrase, nil
	} else {
		if _, err = keys.UnlockService(passphrase); err != nil {
			// Existing user-protected identities stay in place until their
			// owner completes the one-time service-signing migration.
			err = nil
		}
	}
	expected, err = db.ExpectedSigningIdentity(ctx, physical)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(expected)
	if err != nil {
		return "", err
	}
	_, settingErr := db.GetSetting(ctx, "snail_public_identity")
	if settingErr != nil && !errors.Is(settingErr, sql.ErrNoRows) {
		return "", settingErr
	}
	if errors.Is(settingErr, sql.ErrNoRows) {
		err = store.RunInTx(ctx, db.DB, func(ctx context.Context, tx bun.Tx) error {
			if _, err := tx.NewRaw("INSERT INTO offline_actors(id,display_name) VALUES(?,?) ON CONFLICT(id) DO NOTHING", store.SigningServiceActorID, "服务签发身份服务").Exec(ctx); err != nil {
				return err
			}
			if _, err := tx.NewRaw("INSERT INTO settings(key,value) VALUES('snail_public_identity',?) ON CONFLICT(key) DO NOTHING", string(encoded)).Exec(ctx); err != nil {
				return err
			}
			_, err := tx.NewInsert().Model(&model.AuditLog{ID: store.NewID(), Entity: "offline-signing-identity", EntityID: expected.KeyID, Action: "register-existing-identity", ActorID: store.SigningServiceActorID, ActorType: "service", Changes: map[string]any{"keyId": expected.KeyID}}).Exec(ctx)
			return err
		})
		if err != nil {
			return "", err
		}
	}
	return passphrase, nil
}
