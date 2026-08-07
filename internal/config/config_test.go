package config

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"
)

func setAccountLifecycleConfig(t *testing.T) {
	t.Helper()
	setSourceConfig(t)
	t.Setenv("RATE_LIMIT_KEY", base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	t.Setenv("SMTP_ADDRESS", "127.0.0.1:1025")
	t.Setenv("SMTP_FROM_ADDRESS", "workouts@localhost")
	t.Setenv("SMTP_ALLOW_INSECURE_LOCAL", "true")
	t.Setenv("LOCAL_DEVELOPMENT", "true")
}

func setSourceConfig(t *testing.T) {
	t.Helper()
	t.Setenv("SOURCE_KEYRING_FILE", "/var/run/secrets/workouts/keyring.json")
	t.Setenv("LOCAL_SOURCE_ROOTS", "/data/workouts")
}

func TestLoadAPIValidatesPublicConfig(t *testing.T) {
	t.Setenv("API_DATABASE_URL", "postgresql://database.invalid/workouts")
	setAccountLifecycleConfig(t)
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{"zero polling", "UI_POLLING_INTERVAL_SECONDS", "0"},
		{"excessive polling", "UI_POLLING_INTERVAL_SECONDS", "3601"},
		{"negative padding", "MAP_FIT_PADDING_PIXELS", "-1"},
		{"excessive padding", "MAP_FIT_PADDING_PIXELS", "513"},
		{"userinfo", "BASE_MAP_TILE_URL", "https://user@example.com/{z}"},
		{"non-http", "BASE_MAP_TILE_URL", "file:///tiles/{z}"},
		{"loopback", "BASE_MAP_TILE_URL", "http://127.0.0.1/tiles/{z}"},
		{"private address", "BASE_MAP_TILE_URL", "http://10.0.0.1/tiles/{z}"},
		{"link-local address", "BASE_MAP_TILE_URL", "http://169.254.1.1/tiles/{z}"},
		{"internal domain", "BASE_MAP_TILE_URL", "https://tiles.internal/{z}"},
		{"public URL userinfo", "PUBLIC_URL", "https://user@workouts.example.com"},
		{"public URL scheme", "PUBLIC_URL", "javascript:alert(1)"},
		{"public URL non-loopback HTTP", "PUBLIC_URL", "http://workouts.example.com"},
		{"public URL path", "PUBLIC_URL", "https://workouts.example.com/app"},
		{"short session", "SESSION_LIFETIME", "4m"},
		{"long session", "SESSION_LIFETIME", "25h"},
		{"page maximum below default", "PAGE_SIZE_MAXIMUM", "24"},
		{"excessive attribution", "BASE_MAP_ATTRIBUTION", strings.Repeat("x", 1025)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(test.key, test.value)
			if _, err := LoadAPI(); err == nil {
				t.Fatal("hostile public configuration unexpectedly succeeded")
			}
		})
	}
}

func TestLoadAPIRequiresExplicitLoopbackDevelopment(t *testing.T) {
	t.Setenv("API_DATABASE_URL", "postgresql://database.invalid/workouts")
	setAccountLifecycleConfig(t)
	if cfg, err := LoadAPI(); err != nil || cfg.SessionLifetime != 2*time.Hour || !cfg.LocalDevelopment {
		t.Fatalf("validated local config: lifetime=%s local=%t err=%v", cfg.SessionLifetime, cfg.LocalDevelopment, err)
	}
	t.Setenv("LOCAL_DEVELOPMENT", "false")
	if _, err := LoadAPI(); err == nil {
		t.Fatal("HTTP public origin or plaintext SMTP was accepted outside local development")
	}
	t.Setenv("PUBLIC_URL", "http://10.0.0.2")
	t.Setenv("LOCAL_DEVELOPMENT", "true")
	if _, err := LoadAPI(); err == nil {
		t.Fatal("non-loopback HTTP public origin was accepted")
	}
}

func TestLoadAPIAllowsExplicitLocalMapOnlyForLocalDevelopment(t *testing.T) {
	t.Setenv("API_DATABASE_URL", "postgresql://database.invalid/workouts")
	setAccountLifecycleConfig(t)
	t.Setenv("BASE_MAP_TILE_URL", "http://127.0.0.1:9000/tiles/{z}/{x}/{y}")
	t.Setenv("ALLOW_LOCAL_BASE_MAP", "true")
	if _, err := LoadAPI(); err != nil {
		t.Fatalf("explicit local development config failed: %v", err)
	}
	t.Setenv("PUBLIC_URL", "https://workouts.example.com")
	if _, err := LoadAPI(); err == nil {
		t.Fatal("production public URL unexpectedly allowed a local base map")
	}
}

func TestLoadAPIAcceptsSafePublicTemplate(t *testing.T) {
	t.Setenv("API_DATABASE_URL", "postgresql://database.invalid/workouts")
	setAccountLifecycleConfig(t)
	t.Setenv("PUBLIC_URL", "https://workouts.example.com")
	t.Setenv("BASE_MAP_TILE_URL", "https://tiles.example.com/{z}/{x}/{y}.png")
	if _, err := LoadAPI(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAPIProductionTransportAndSessionLifetime(t *testing.T) {
	t.Setenv("API_DATABASE_URL", "postgresql://database.invalid/workouts")
	setSourceConfig(t)
	t.Setenv("RATE_LIMIT_KEY", base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	t.Setenv("PUBLIC_URL", "https://workouts.example.com")
	t.Setenv("SESSION_LIFETIME", "90m")
	t.Setenv("SMTP_ADDRESS", "smtp.example.com:587")
	t.Setenv("SMTP_USERNAME", "workouts")
	t.Setenv("SMTP_FROM_ADDRESS", "workouts@example.com")
	passwordFile := t.TempDir() + "/smtp-password"
	if err := os.WriteFile(passwordFile, []byte("smtp password"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SMTP_PASSWORD_FILE", passwordFile)
	cfg, err := LoadAPI()
	if err != nil || cfg.LocalDevelopment || cfg.SessionLifetime != 90*time.Minute {
		t.Fatalf("production config lifetime=%s local=%t err=%v", cfg.SessionLifetime, cfg.LocalDevelopment, err)
	}
	t.Setenv("SMTP_ALLOW_INSECURE_LOCAL", "true")
	if _, err := LoadAPI(); err == nil {
		t.Fatal("production accepted plaintext SMTP")
	}
}

func TestLoadCommonValidatesSourceConfiguration(t *testing.T) {
	t.Setenv("API_DATABASE_URL", "postgresql://database.invalid/workouts")
	setAccountLifecycleConfig(t)
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{"missing keyring", "SOURCE_KEYRING_FILE", ""},
		{"relative keyring", "SOURCE_KEYRING_FILE", "keyring.json"},
		{"missing roots", "LOCAL_SOURCE_ROOTS", ""},
		{"relative root", "LOCAL_SOURCE_ROOTS", "data/workouts"},
		{"filesystem root", "LOCAL_SOURCE_ROOTS", "/"},
		{"duplicate roots", "LOCAL_SOURCE_ROOTS", "/data/workouts,/data/workouts"},
		{"overlapping roots", "LOCAL_SOURCE_ROOTS", "/data,/data/workouts"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(test.key, test.value)
			if _, err := LoadAPI(); err == nil {
				t.Fatal("invalid source runtime configuration accepted")
			}
		})
	}
}

func TestLoadAPIRestrictsPlaintextSMTPToMailpit(t *testing.T) {
	t.Setenv("API_DATABASE_URL", "postgresql://database.invalid/workouts")
	setAccountLifecycleConfig(t)
	t.Setenv("SMTP_ADDRESS", "mail.internal:1025")
	if _, err := LoadAPI(); err == nil {
		t.Fatal("non-Mailpit internal SMTP was accepted in plaintext")
	}
	t.Setenv("SMTP_ADDRESS", "mailpit.workouts-explorer.svc.cluster.local:1025")
	if _, err := LoadAPI(); err != nil {
		t.Fatalf("validated development Mailpit failed: %v", err)
	}
}

func TestLoadWorkerConcurrencyAndStaging(t *testing.T) {
	t.Setenv("WORKER_DATABASE_URL", "postgresql://database.invalid/workouts")
	setSourceConfig(t)
	cfg, err := LoadWorker()
	if err != nil || cfg.FileConcurrency != 2 || cfg.AccountConcurrency != 2 || cfg.GlobalConcurrency != 4 || cfg.StagingRoot != "/var/lib/workouts/staging" ||
		cfg.AutoSyncInterval != 24*time.Hour || cfg.AutoSyncPollInterval != 30*time.Second || cfg.AutoSyncStaleDays != 3 || cfg.SchedulerLease != 2*time.Minute {
		t.Fatalf("defaults=%+v err=%v", cfg, err)
	}
	for _, test := range []struct{ key, value string }{
		{"WORKER_FILE_CONCURRENCY", "0"}, {"WORKER_FILE_CONCURRENCY", "17"},
		{"ACCOUNT_FILE_CONCURRENCY", "5"}, {"GLOBAL_FILE_CONCURRENCY", "17"},
		{"WORKER_STAGING_ROOT", "relative"}, {"WORKER_STAGING_ROOT", "/"},
		{"AUTO_SYNC_INTERVAL", "4m"}, {"AUTO_SYNC_INTERVAL", "169h"},
		{"AUTO_SYNC_POLL_INTERVAL", "500ms"}, {"AUTO_SYNC_POLL_INTERVAL", "6m"},
		{"AUTO_SYNC_STALE_DAYS", "0"}, {"AUTO_SYNC_STALE_DAYS", "31"},
		{"SCHEDULER_LEASE_DURATION", "30s"},
		{"SCHEDULER_LEASE_DURATION", "901s"}, {"SCHEDULER_LEASE_DURATION", "999s"},
		{"SCHEDULER_LEASE_DURATION", "3600s"}, {"SCHEDULER_LEASE_DURATION", "16m"},
		{"SCHEDULER_LEASE_DURATION", "1h"},
	} {
		t.Run(test.key+test.value, func(t *testing.T) {
			t.Setenv(test.key, test.value)
			if test.key == "ACCOUNT_FILE_CONCURRENCY" {
				t.Setenv("GLOBAL_FILE_CONCURRENCY", "4")
			}
			if _, err := LoadWorker(); err == nil {
				t.Fatal("invalid worker configuration accepted")
			}
		})
	}
	for _, lease := range []string{"900s", "15m"} {
		t.Run("valid lease "+lease, func(t *testing.T) {
			t.Setenv("SCHEDULER_LEASE_DURATION", lease)
			if _, err := LoadWorker(); err != nil {
				t.Fatalf("valid scheduler lease %s rejected: %v", lease, err)
			}
		})
	}
}
