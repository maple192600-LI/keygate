package config

import (
	"strings"
	"testing"
)

func TestLocalOfflineConfig(t *testing.T) {
	t.Setenv("SNAIL_DEPLOYMENT", "local_offline")
	t.Setenv("SNAIL_RUNTIME_SHUTDOWN_TOKEN", strings.Repeat("c", 64))
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("PORT", "9000")
	t.Setenv("BASE_URL", "http://127.0.0.1:9000")
	t.Setenv("DATABASE_URL", "postgres://local:synthetic@127.0.0.1:55432/local?sslmode=disable")
	t.Setenv("JWT_SECRET", strings.Repeat("a", 64))
	t.Setenv("RELEASE_KEY_ENCRYPTION_KEY", strings.Repeat("b", 64))
	t.Setenv("LICENSE_SIGNING_KEY", "")
	t.Setenv("ADMIN_EMAILS", "")
	t.Setenv("REDIS_URL", "")
	t.Setenv("SMTP_HOST", "")
	t.Setenv("STRIPE_SECRET_KEY", "")
	t.Setenv("STORAGE_BUCKET", "")
	t.Setenv("STORAGE_ACCESS_KEY", "")
	t.Setenv("STORAGE_SECRET_KEY", "")
	t.Setenv("STORAGE_ENDPOINT", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	warnings, fatal := cfg.ValidateSecurityDefaults()
	if len(fatal) != 0 {
		t.Fatalf("local configuration requires unused online identity: %v", fatal)
	}
	for _, w := range warnings {
		if strings.Contains(w, "ADMIN_EMAILS") {
			t.Fatal("fake email required")
		}
	}
	cfg.BaseURL = "http://0.0.0.0:9000"
	_, fatal = cfg.ValidateSecurityDefaults()
	if len(fatal) == 0 {
		t.Fatal("nonlocal address accepted")
	}
	cfg.BaseURL = "http://127.0.0.1:9000"
	cfg.Environment = "development"
	_, fatal = cfg.ValidateSecurityDefaults()
	if len(fatal) == 0 {
		t.Fatal("development bypass accepted")
	}
	cfg.Environment = "production"
	cfg.SnailRuntimeToken = ""
	_, fatal = cfg.ValidateSecurityDefaults()
	if len(fatal) == 0 {
		t.Fatal("missing native runtime identity accepted")
	}
	cfg.SnailRuntimeToken = strings.Repeat("c", 64)
	cfg.SMTPHost = "mail.invalid"
	_, fatal = cfg.ValidateSecurityDefaults()
	if len(fatal) == 0 {
		t.Fatal("online service accepted")
	}
}
