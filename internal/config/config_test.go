package config

import (
	"encoding/base64"
	"os"
	"reflect"
	"strconv"
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

func TestOTLPEndpointDefaultsAndOverrides(t *testing.T) {
	t.Setenv("API_DATABASE_URL", "postgresql://database.invalid/workouts")
	t.Setenv("WORKER_DATABASE_URL", "postgresql://database.invalid/workouts")
	t.Setenv("OSM_DATABASE_URL", "postgresql://database.invalid/osm")
	t.Setenv("COVERAGE_WORKER_DATABASE_URL", "postgresql://database.invalid/workouts")
	t.Setenv("COVERAGE_WORKER_OSM_DATABASE_URL", "postgresql://database.invalid/osm")
	setAccountLifecycleConfig(t)
	for _, test := range []struct {
		name     string
		endpoint string
		unset    bool
	}{
		{"default", "grpcs://otel-gateway-collector.observability.svc.cluster.local:4317", true},
		{"explicitly disabled", "", false},
		{"legacy HTTP", "http://collector:4328", false},
		{"legacy HTTPS", "https://collector:4318", false},
		{"TLS gRPC override", "grpcs://collector:4317", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", test.endpoint)
			if test.unset {
				if err := os.Unsetenv("OTEL_EXPORTER_OTLP_ENDPOINT"); err != nil {
					t.Fatal(err)
				}
			}
			api, apiErr := LoadAPI()
			worker, workerErr := LoadWorker()
			coverage, coverageErr := LoadCoverageWorker()
			if apiErr != nil || workerErr != nil || coverageErr != nil {
				t.Fatalf("load configs: api=%v worker=%v coverage=%v", apiErr, workerErr, coverageErr)
			}
			if api.OTLPEndpoint != test.endpoint || worker.OTLPEndpoint != test.endpoint || coverage.OTLPEndpoint != test.endpoint {
				t.Fatalf("endpoints: api=%q worker=%q coverage=%q; want %q", api.OTLPEndpoint, worker.OTLPEndpoint, coverage.OTLPEndpoint, test.endpoint)
			}
		})
	}
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
		{"public URL userinfo", "PUBLIC_URL", "https://user@workouts.example.com"},
		{"public URL scheme", "PUBLIC_URL", "javascript:alert(1)"},
		{"public URL non-loopback HTTP", "PUBLIC_URL", "http://workouts.example.com"},
		{"public URL path", "PUBLIC_URL", "https://workouts.example.com/app"},
		{"short session", "SESSION_LIFETIME", "4m"},
		{"long session", "SESSION_LIFETIME", "25h"},
		{"page maximum below default", "PAGE_SIZE_MAXIMUM", "24"},
		{"tile server userinfo", "TILE_SERVER_URL", "http://user@workouts-explorer-tiles:3000"},
		{"tile server scheme", "TILE_SERVER_URL", "file:///tiles"},
		{"tile server query", "TILE_SERVER_URL", "http://workouts-explorer-tiles:3000?account=foreign"},
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
	if cfg, err := LoadAPI(); err != nil || cfg.SessionLifetime != 6*time.Hour || !cfg.LocalDevelopment {
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

func TestLoadAPIAcceptsBaseMapDefault(t *testing.T) {
	t.Setenv("API_DATABASE_URL", "postgresql://database.invalid/workouts")
	setAccountLifecycleConfig(t)
	t.Setenv("PUBLIC_URL", "https://workouts.example.com")
	cfg, err := LoadAPI()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.BaseMaps.StyleFamilies) != 1 || cfg.BaseMaps.FallbackFamilyID != "local-placeholder" || !reflect.DeepEqual(cfg.BrowserSigninOrigins, []string{"https://workouts.example.com"}) || cfg.SessionLifetime != 6*time.Hour {
		t.Fatalf("unexpected safe base-map default: %+v", cfg.BaseMaps)
	}
}

func TestLoadAPIAcceptsExplicitBrowserSigninOrigins(t *testing.T) {
	t.Setenv("API_DATABASE_URL", "postgresql://database.invalid/workouts")
	setAccountLifecycleConfig(t)
	t.Setenv("PUBLIC_URL", "https://workouts.example.com")
	t.Setenv("BROWSER_SIGNIN_ORIGINS", "https://workouts.example.com, https://workouts.internal.example")
	cfg, err := LoadAPI()
	if err != nil || !reflect.DeepEqual(cfg.BrowserSigninOrigins, []string{"https://workouts.example.com", "https://workouts.internal.example"}) {
		t.Fatalf("browser sign-in origins=%v err=%v", cfg.BrowserSigninOrigins, err)
	}
	for _, invalid := range []string{
		"https://workouts.internal.example",
		"https://workouts.example.com,https://workouts.example.com",
		"https://workouts.example.com,https://workouts.internal.example/path",
		"https://workouts.example.com,http://workouts.internal.example",
	} {
		t.Run(invalid, func(t *testing.T) {
			t.Setenv("BROWSER_SIGNIN_ORIGINS", invalid)
			if _, err := LoadAPI(); err == nil {
				t.Fatal("invalid browser sign-in origins accepted")
			}
		})
	}
}

func TestLoadAPICoverageDiagnostics(t *testing.T) {
	t.Setenv("API_DATABASE_URL", "postgresql://database.invalid/workouts")
	setAccountLifecycleConfig(t)
	cfg, err := LoadAPI()
	if err != nil || cfg.CoverageDiagnostics.Enabled || cfg.CoverageDiagnostics.Timeout != 120*time.Second ||
		cfg.CoverageDiagnostics.Concurrency != 1 || cfg.CoverageDiagnostics.MinimumTraversalMeters != 5 ||
		cfg.CoverageDiagnostics.MotorLaneWidthMeters != 3 || cfg.CoverageDiagnostics.BicycleLaneWidthMeters != 1.5 ||
		cfg.CoverageDiagnostics.ParkingLaneWidthMeters != 2.1 || cfg.CoverageDiagnostics.SidewalkSetbackMeters != 3 ||
		cfg.CoverageDiagnostics.DirectionalDriftMeters != 7 || cfg.CoverageDiagnostics.DeadEndEndpointAllowanceMeters != 3 {
		t.Fatalf("diagnostic defaults=%+v err=%v", cfg.CoverageDiagnostics, err)
	}
	t.Setenv("COVERAGE_DIAGNOSTICS_ENABLED", "true")
	if _, err := LoadAPI(); err == nil {
		t.Fatal("enabled diagnostics accepted without API_OSM_DATABASE_URL")
	}
	t.Setenv("API_OSM_DATABASE_URL", "postgresql://database.invalid/osm")
	t.Setenv("COVERAGE_DIAGNOSTICS_TIMEOUT", "25s")
	t.Setenv("COVERAGE_DIAGNOSTICS_CONCURRENCY", "4")
	t.Setenv("COVERAGE_MIN_TRAVERSAL_METERS", "7.5")
	t.Setenv("COVERAGE_MOTOR_LANE_WIDTH_METERS", "3.2")
	t.Setenv("COVERAGE_BICYCLE_LANE_WIDTH_METERS", "1.7")
	t.Setenv("COVERAGE_PARKING_LANE_WIDTH_METERS", "2.2")
	t.Setenv("COVERAGE_SIDEWALK_SETBACK_METERS", "3.5")
	t.Setenv("COVERAGE_DIRECTIONAL_DRIFT_METERS", "6")
	t.Setenv("COVERAGE_DEAD_END_ENDPOINT_ALLOWANCE_METERS", "4")
	if cfg, err = LoadAPI(); err != nil || !cfg.CoverageDiagnostics.Enabled || cfg.CoverageDiagnostics.Timeout != 25*time.Second ||
		cfg.CoverageDiagnostics.Concurrency != 4 || cfg.CoverageDiagnostics.MinimumTraversalMeters != 7.5 ||
		cfg.CoverageDiagnostics.MotorLaneWidthMeters != 3.2 || cfg.CoverageDiagnostics.BicycleLaneWidthMeters != 1.7 ||
		cfg.CoverageDiagnostics.ParkingLaneWidthMeters != 2.2 || cfg.CoverageDiagnostics.SidewalkSetbackMeters != 3.5 ||
		cfg.CoverageDiagnostics.DirectionalDriftMeters != 6 || cfg.CoverageDiagnostics.DeadEndEndpointAllowanceMeters != 4 {
		t.Fatalf("diagnostic config=%+v err=%v", cfg.CoverageDiagnostics, err)
	}
	for _, invalid := range []struct{ name, value string }{
		{"COVERAGE_DIAGNOSTICS_ENABLED", "perhaps"},
		{"COVERAGE_DIAGNOSTICS_TIMEOUT", "4s"},
		{"COVERAGE_DIAGNOSTICS_TIMEOUT", "121s"},
		{"COVERAGE_DIAGNOSTICS_CONCURRENCY", "0"},
		{"COVERAGE_DIAGNOSTICS_CONCURRENCY", "5"},
		{"COVERAGE_MIN_TRAVERSAL_METERS", "0"},
		{"COVERAGE_MOTOR_LANE_WIDTH_METERS", "2.4"},
		{"COVERAGE_BICYCLE_LANE_WIDTH_METERS", "2.6"},
		{"COVERAGE_PARKING_LANE_WIDTH_METERS", "1.4"},
		{"COVERAGE_SIDEWALK_SETBACK_METERS", "5.1"},
		{"COVERAGE_DIRECTIONAL_DRIFT_METERS", "10.1"},
		{"COVERAGE_DEAD_END_ENDPOINT_ALLOWANCE_METERS", "10.1"},
	} {
		t.Run(invalid.name+invalid.value, func(t *testing.T) {
			t.Setenv(invalid.name, invalid.value)
			if _, err := LoadAPI(); err == nil {
				t.Fatal("invalid diagnostics configuration accepted")
			}
		})
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

func TestLoadAPIRestrictsPlaintextSMTPToLoopback(t *testing.T) {
	t.Setenv("API_DATABASE_URL", "postgresql://database.invalid/workouts")
	setAccountLifecycleConfig(t)
	t.Setenv("SMTP_ADDRESS", "mail.internal:1025")
	if _, err := LoadAPI(); err == nil {
		t.Fatal("internal SMTP was accepted in plaintext")
	}
	t.Setenv("SMTP_ADDRESS", "smtp.workouts-explorer.svc.cluster.local:1025")
	if _, err := LoadAPI(); err == nil {
		t.Fatal("cluster SMTP was accepted in plaintext")
	}
	t.Setenv("SMTP_ADDRESS", "127.0.0.1:1025")
	if _, err := LoadAPI(); err != nil {
		t.Fatalf("loopback development SMTP failed: %v", err)
	}
}

func TestLoadCoverageWorker(t *testing.T) {
	t.Setenv("COVERAGE_WORKER_DATABASE_URL", "postgresql://coverage.invalid/workouts")
	t.Setenv("COVERAGE_WORKER_OSM_DATABASE_URL", "postgresql://coverage.invalid/osm")
	cfg, err := LoadCoverageWorker()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddress != ":8082" || cfg.LeaseDuration != 7*time.Minute || cfg.HeartbeatInterval != 20*time.Second ||
		cfg.RouteTimeout != 6*time.Minute || cfg.PollInterval != time.Second || cfg.AdmissionInterval != time.Second ||
		cfg.ReconciliationPollInterval != 30*time.Second || cfg.ReconciliationScanInterval != 24*time.Hour ||
		cfg.ReconciliationPageSize != 10 || cfg.MinimumTraversalMeters != 5 || cfg.OSMAutoAddRegions || cfg.JobConcurrency != 2 {
		t.Fatalf("unexpected coverage worker defaults: %+v", cfg)
	}
	t.Setenv("OSM_AUTO_ADD_REGIONS", "true")
	configured, err := LoadCoverageWorker()
	if err != nil || !configured.OSMAutoAddRegions {
		t.Fatalf("coverage worker auto-add policy was not loaded: %+v err=%v", configured, err)
	}
}

func TestLoadCoverageWorkerJobConcurrency(t *testing.T) {
	t.Setenv("COVERAGE_WORKER_DATABASE_URL", "postgresql://coverage.invalid/workouts")
	t.Setenv("COVERAGE_WORKER_OSM_DATABASE_URL", "postgresql://coverage.invalid/osm")
	t.Setenv("API_DATABASE_URL", "postgresql://api.invalid/workouts")
	t.Setenv("COVERAGE_DIAGNOSTICS_CONCURRENCY", "3")
	setAccountLifecycleConfig(t)
	for _, raw := range []string{"", "1", "2", "4", "8"} {
		t.Run("valid="+raw, func(t *testing.T) {
			t.Setenv("COVERAGE_WORKER_JOB_CONCURRENCY", raw)
			want := 2
			if raw != "" {
				want, _ = strconv.Atoi(raw)
			}
			cfg, err := LoadCoverageWorker()
			if err != nil || cfg.JobConcurrency != want {
				t.Fatalf("job concurrency=%d want %d err=%v", cfg.JobConcurrency, want, err)
			}
			api, err := LoadAPI()
			if err != nil || api.CoverageDiagnostics.Concurrency != 3 {
				t.Fatalf("independent diagnostics concurrency=%d err=%v", api.CoverageDiagnostics.Concurrency, err)
			}
		})
	}
	for _, raw := range []string{"0", "-1", "9", "1.5", " ", "999999999999999999999999", "postgresql://fake-user:fake-secret@fixture.invalid/workouts"} {
		t.Run("invalid="+raw, func(t *testing.T) {
			t.Setenv("COVERAGE_WORKER_JOB_CONCURRENCY", raw)
			_, err := LoadCoverageWorker()
			if err == nil || err.Error() != "COVERAGE_WORKER_JOB_CONCURRENCY must be an integer between 1 and 8" {
				t.Fatalf("expected safe range validation error, got %v", err)
			}
		})
	}
}

func TestLoadCoverageWorkerAutoRetryCount(t *testing.T) {
	t.Setenv("COVERAGE_WORKER_DATABASE_URL", "postgresql://coverage.invalid/workouts")
	t.Setenv("COVERAGE_WORKER_OSM_DATABASE_URL", "postgresql://coverage.invalid/osm")
	for _, raw := range []string{"", "0", "1", "2", "20"} {
		t.Run("valid="+raw, func(t *testing.T) {
			t.Setenv("COVERAGE_WORKER_AUTO_RETRY_COUNT", raw)
			want := 1
			if raw != "" {
				want, _ = strconv.Atoi(raw)
			}
			cfg, err := LoadCoverageWorker()
			if err != nil || cfg.AutoRetryCount != want {
				t.Fatalf("automatic retries=%d want=%d err=%v", cfg.AutoRetryCount, want, err)
			}
		})
	}
	for _, raw := range []string{"-1", "21", "1.5", " ", "999999999999999999999999", "private-value"} {
		t.Run("invalid="+raw, func(t *testing.T) {
			t.Setenv("COVERAGE_WORKER_AUTO_RETRY_COUNT", raw)
			_, err := LoadCoverageWorker()
			if err == nil || err.Error() != "COVERAGE_WORKER_AUTO_RETRY_COUNT must be an integer between 0 and 20" {
				t.Fatalf("expected safe range error, got %v", err)
			}
		})
	}
}

func TestLoadWorkerCoverageBatchSize(t *testing.T) {
	t.Setenv("WORKER_DATABASE_URL", "postgresql://database.invalid/workouts")
	t.Setenv("OSM_DATABASE_URL", "postgresql://database.invalid/osm")
	setSourceConfig(t)
	for _, raw := range []string{"", "1", "25", "1000", "0", "1001", "-1", "1.5", "private-value"} {
		t.Run("value="+raw, func(t *testing.T) {
			t.Setenv("WORKER_COVERAGE_BATCH_SIZE", raw)
			want := 25
			if raw != "" {
				want, _ = strconv.Atoi(raw)
			}
			cfg, err := LoadWorker()
			valid := raw == "" || (want >= 1 && want <= 1000 && raw != "1.5")
			if valid && (err != nil || cfg.CoverageBatchSize != want) {
				t.Fatalf("batch size=%d want=%d err=%v", cfg.CoverageBatchSize, want, err)
			}
			if !valid && (err == nil || err.Error() != "WORKER_COVERAGE_BATCH_SIZE must be an integer between 1 and 1000") {
				t.Fatalf("expected safe range error, got %v", err)
			}
		})
	}
}

func TestLoadCoverageWorkerValidatesLeaseBudget(t *testing.T) {
	t.Setenv("COVERAGE_WORKER_DATABASE_URL", "postgresql://coverage.invalid/workouts")
	t.Setenv("COVERAGE_WORKER_OSM_DATABASE_URL", "postgresql://coverage.invalid/osm")
	for _, test := range []struct{ key, value string }{
		{"COVERAGE_WORKER_HEARTBEAT_INTERVAL", "210s"},
		{"COVERAGE_WORKER_ROUTE_TIMEOUT", "410s"},
		{"COVERAGE_WORKER_ROUTE_TIMEOUT", "415s"},
		{"COVERAGE_WORKER_LEASE_DURATION", "240s"},
	} {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			t.Setenv(test.key, test.value)
			if _, err := LoadCoverageWorker(); err == nil {
				t.Fatal("unsafe coverage worker timing was accepted")
			}
		})
	}
}

func TestLoadCoverageWorkerRouteTimeoutRange(t *testing.T) {
	t.Setenv("COVERAGE_WORKER_DATABASE_URL", "postgresql://coverage.invalid/workouts")
	t.Setenv("COVERAGE_WORKER_OSM_DATABASE_URL", "postgresql://coverage.invalid/osm")
	t.Setenv("COVERAGE_WORKER_LEASE_DURATION", "15m")
	for _, test := range []struct {
		value string
		valid bool
	}{
		{"5s", true}, {"2m", true}, {"6m", true}, {"10m", true},
		{"-1s", false}, {"0s", false}, {"2s", false}, {"601s", false}, {"12m", false}, {"15m", false},
	} {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv("COVERAGE_WORKER_ROUTE_TIMEOUT", test.value)
			cfg, err := LoadCoverageWorker()
			if (err == nil) != test.valid {
				t.Fatalf("valid=%t, want %t: %v", err == nil, test.valid, err)
			}
			if test.valid {
				want, _ := time.ParseDuration(test.value)
				if cfg.RouteTimeout != want {
					t.Errorf("initial timeout=%s, want %s", cfg.RouteTimeout, want)
				}
			}
		})
	}
}

func TestLoadWorkerConcurrencyAndStaging(t *testing.T) {
	t.Setenv("WORKER_DATABASE_URL", "postgresql://database.invalid/workouts")
	t.Setenv("OSM_DATABASE_URL", "postgresql://database.invalid/osm")
	setSourceConfig(t)
	cfg, err := LoadWorker()
	if err != nil || cfg.FileConcurrency != 2 || cfg.AccountConcurrency != 2 || cfg.GlobalConcurrency != 4 || cfg.StagingRoot != "/var/lib/workouts/staging" ||
		cfg.AutoSyncInterval != 24*time.Hour || cfg.AutoSyncPollInterval != 30*time.Second || cfg.AutoSyncStaleDays != 3 || cfg.SchedulerLease != 2*time.Minute ||
		cfg.CoverageMinTraversalMeters != 5 ||
		cfg.OSM.AutoAddRegions || cfg.OSM.MaxAutoDownloadBytes != 1<<30 || !reflect.DeepEqual(cfg.OSM.DataProviders, []string{"geofabrik"}) ||
		!reflect.DeepEqual(cfg.OSM.Regions, []string{"geofabrik:norcal"}) {
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
		{"COVERAGE_MIN_TRAVERSAL_METERS", "0"}, {"COVERAGE_MIN_TRAVERSAL_METERS", "101"}, {"COVERAGE_MIN_TRAVERSAL_METERS", "NaN"},
		{"OSM_AUTO_ADD_REGIONS", "sometimes"},
		{"OSM_MAX_AUTO_DOWNLOAD_BYTES", "0"}, {"OSM_MAX_AUTO_DOWNLOAD_BYTES", "1099511627777"},
		{"OSM_DATA_PROVIDERS_JSON", `{}`}, {"OSM_DATA_PROVIDERS_JSON", `["geofabrik","geofabrik"]`},
		{"OSM_DATA_PROVIDERS_JSON", `["geofabrik:norcal"]`},
		{"OSM_REGIONS_JSON", `["norcal"]`}, {"OSM_REGIONS_JSON", `["geofabrik:nor_cal"]`},
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
	t.Run("custom osm settings", func(t *testing.T) {
		t.Setenv("OSM_AUTO_ADD_REGIONS", "true")
		t.Setenv("OSM_MAX_AUTO_DOWNLOAD_BYTES", "2000000000")
		t.Setenv("OSM_DATA_PROVIDERS_JSON", `["geofabrik","custom-provider"]`)
		t.Setenv("OSM_REGIONS_JSON", `["geofabrik:norcal","geofabrik:new-york"]`)
		cfg, err := LoadWorker()
		if err != nil || !cfg.OSM.AutoAddRegions || cfg.OSM.MaxAutoDownloadBytes != 2_000_000_000 ||
			!reflect.DeepEqual(cfg.OSM.DataProviders, []string{"geofabrik", "custom-provider"}) ||
			!reflect.DeepEqual(cfg.OSM.Regions, []string{"geofabrik:norcal", "geofabrik:new-york"}) {
			t.Fatalf("custom OSM config=%+v err=%v", cfg.OSM, err)
		}
	})
	t.Run("custom coverage minimum", func(t *testing.T) {
		t.Setenv("COVERAGE_MIN_TRAVERSAL_METERS", "7.5")
		cfg, err := LoadWorker()
		if err != nil || cfg.CoverageMinTraversalMeters != 7.5 {
			t.Fatalf("custom coverage minimum=%v err=%v", cfg.CoverageMinTraversalMeters, err)
		}
	})
	for _, lease := range []string{"900s", "15m"} {
		t.Run("valid lease "+lease, func(t *testing.T) {
			t.Setenv("SCHEDULER_LEASE_DURATION", lease)
			if _, err := LoadWorker(); err != nil {
				t.Fatalf("valid scheduler lease %s rejected: %v", lease, err)
			}
		})
	}
}

func TestOSMUseReadReplicasOnly(t *testing.T) {
	t.Setenv("API_DATABASE_URL", "postgresql://api.invalid/workouts")
	t.Setenv("API_OSM_DATABASE_URL", "postgresql://api.invalid/osm")
	t.Setenv("WORKER_DATABASE_URL", "postgresql://worker.invalid/workouts")
	t.Setenv("OSM_DATABASE_URL", "postgresql://worker.invalid/osm")
	t.Setenv("COVERAGE_WORKER_DATABASE_URL", "postgresql://coverage.invalid/workouts")
	t.Setenv("COVERAGE_WORKER_OSM_DATABASE_URL", "postgresql://coverage.invalid/osm")
	t.Setenv("OSM_AUTO_ADD_REGIONS", "false")
	t.Setenv("OSM_USE_READ_REPLICAS_ONLY", "")
	setAccountLifecycleConfig(t)

	for _, diagnosticsEnabled := range []string{"false", "true"} {
		t.Run("diagnostics="+diagnosticsEnabled, func(t *testing.T) {
			t.Setenv("COVERAGE_DIAGNOSTICS_ENABLED", diagnosticsEnabled)
			if diagnosticsEnabled == "false" {
				t.Setenv("API_OSM_DATABASE_URL", "")
			}
			apiDefault, apiErr := LoadAPI()
			workerDefault, workerErr := LoadWorker()
			coverageDefault, coverageErr := LoadCoverageWorker()
			osmDefault, osmErr := LoadOSM()
			if apiErr != nil || workerErr != nil || coverageErr != nil || osmErr != nil {
				t.Fatalf("defaults: api=%v worker=%v coverage=%v osm=%v", apiErr, workerErr, coverageErr, osmErr)
			}
			if apiDefault.CoverageDiagnostics.UseReadReplicasOnly || workerDefault.OSM.UseReadReplicasOnly || coverageDefault.UseReadReplicasOnly || osmDefault.UseReadReplicasOnly {
				t.Fatal("reader-only mode must default to false")
			}
			for _, tc := range []struct {
				value string
				want  bool
			}{
				{"", false}, {"false", false}, {"true", true},
			} {
				t.Run("flag="+tc.value, func(t *testing.T) {
					t.Setenv("OSM_USE_READ_REPLICAS_ONLY", tc.value)
					api, apiErr := LoadAPI()
					worker, workerErr := LoadWorker()
					coverage, coverageErr := LoadCoverageWorker()
					osm, osmErr := LoadOSM()
					if apiErr != nil || workerErr != nil || coverageErr != nil || osmErr != nil {
						t.Fatalf("load: api=%v worker=%v coverage=%v osm=%v", apiErr, workerErr, coverageErr, osmErr)
					}
					if api.CoverageDiagnostics.UseReadReplicasOnly != tc.want || worker.OSM.UseReadReplicasOnly != tc.want || coverage.UseReadReplicasOnly != tc.want || osm.UseReadReplicasOnly != tc.want {
						t.Fatalf("reader-only fields do not match %t", tc.want)
					}
					api.CoverageDiagnostics.UseReadReplicasOnly = false
					worker.OSM.UseReadReplicasOnly = false
					coverage.UseReadReplicasOnly = false
					osm.UseReadReplicasOnly = false
					if !reflect.DeepEqual(api, apiDefault) || !reflect.DeepEqual(worker, workerDefault) || !reflect.DeepEqual(coverage, coverageDefault) || !reflect.DeepEqual(osm, osmDefault) {
						t.Fatal("reader-only flag changed unrelated configuration or database URLs")
					}
				})
			}
			t.Run("malformed", func(t *testing.T) {
				t.Setenv("OSM_USE_READ_REPLICAS_ONLY", "postgresql://fake-user:fake-secret@fixture.invalid/osm")
				_, apiErr := LoadAPI()
				_, workerErr := LoadWorker()
				_, coverageErr := LoadCoverageWorker()
				_, osmErr := LoadOSM()
				for name, err := range map[string]error{"api": apiErr, "worker": workerErr, "coverage": coverageErr, "osm": osmErr} {
					if err == nil || err.Error() != "OSM_USE_READ_REPLICAS_ONLY must be a boolean" {
						t.Errorf("%s: expected safe boolean validation error, got %v", name, err)
					}
				}
			})
		})
	}
}

func TestOSMReadReplicasRejectAutoAddRegions(t *testing.T) {
	t.Setenv("WORKER_DATABASE_URL", "postgresql://worker.invalid/workouts")
	t.Setenv("OSM_DATABASE_URL", "postgresql://worker.invalid/osm")
	t.Setenv("COVERAGE_WORKER_DATABASE_URL", "postgresql://coverage.invalid/workouts")
	t.Setenv("COVERAGE_WORKER_OSM_DATABASE_URL", "postgresql://coverage.invalid/osm")
	t.Setenv("OSM_AUTO_ADD_REGIONS", "true")
	setSourceConfig(t)
	for _, readOnly := range []string{"false", "true"} {
		t.Run(readOnly, func(t *testing.T) {
			t.Setenv("OSM_USE_READ_REPLICAS_ONLY", readOnly)
			_, workerErr := LoadWorker()
			_, coverageErr := LoadCoverageWorker()
			_, osmErr := LoadOSM()
			for name, err := range map[string]error{"worker": workerErr, "coverage": coverageErr, "osm": osmErr} {
				if readOnly == "false" {
					if err != nil {
						t.Errorf("%s: existing auto-add configuration rejected: %v", name, err)
					}
				} else if err == nil || err.Error() != "OSM_USE_READ_REPLICAS_ONLY must not be enabled with OSM_AUTO_ADD_REGIONS" {
					t.Errorf("%s: expected auto-add conflict, got %v", name, err)
				}
			}
		})
	}

	t.Setenv("API_DATABASE_URL", "postgresql://api.invalid/workouts")
	t.Setenv("API_OSM_DATABASE_URL", "postgresql://api.invalid/osm")
	t.Setenv("OSM_USE_READ_REPLICAS_ONLY", "true")
	t.Setenv("COVERAGE_DIAGNOSTICS_ENABLED", "true")
	setAccountLifecycleConfig(t)
	if cfg, err := LoadAPI(); err != nil || !cfg.CoverageDiagnostics.UseReadReplicasOnly {
		t.Fatalf("read-only API diagnostics must not inherit auto-add restrictions: %v", err)
	}
}

func TestLoadOSMDoesNotRequireCommonConfiguration(t *testing.T) {
	for _, name := range []string{"API_DATABASE_URL", "WORKER_DATABASE_URL", "OSM_DATABASE_URL", "SOURCE_KEYRING_FILE", "LOCAL_SOURCE_ROOTS"} {
		t.Setenv(name, "")
	}
	t.Setenv("OSM_AUTO_ADD_REGIONS", "false")
	t.Setenv("OSM_USE_READ_REPLICAS_ONLY", "true")
	if cfg, err := LoadOSM(); err != nil || !cfg.UseReadReplicasOnly {
		t.Fatalf("standalone OSM configuration: %v", err)
	}
}
