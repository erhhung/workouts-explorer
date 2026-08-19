package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
)

const (
	MaxRequestBodyBytes int64 = 1 << 20
	minPollingSeconds         = 1
	maxPollingSeconds         = 3600
	maxMapPaddingPixels       = 512
)

type Common struct {
	DatabaseURL       string
	ListenAddress     string
	OTLPEndpoint      string
	SourceKeyringFile string
	LocalSourceRoots  []string
}

type API struct {
	Common
	PublicURL              string
	PollingIntervalSeconds int
	MapFitPaddingPixels    int
	BaseMaps               BaseMaps
	PGTileServURL          string
	SessionLifetime        time.Duration
	PasswordMinimum        int
	PageSizeMaximum        int
	RateLimitKey           []byte
	TrustedProxyCIDRs      []*net.IPNet
	LocalDevelopment       bool
	SMTP                   SMTP
}

type Worker struct {
	Common
	OSMDatabaseURL       string
	FileConcurrency      int
	AccountConcurrency   int
	GlobalConcurrency    int
	StagingRoot          string
	AutoSyncInterval     time.Duration
	AutoSyncPollInterval time.Duration
	AutoSyncStaleDays    int
	SchedulerLease       time.Duration
	OSM                  OSM
}

type OSM struct {
	AutoAddRegions       bool
	MaxAutoDownloadBytes int64
	DataProviders        []string
	Regions              []string
}

type SMTP struct {
	Address            string
	Username           string
	PasswordFile       string
	FromAddress        string
	AllowInsecureLocal bool
}

func LoadAPI() (API, error) {
	c, err := loadCommon("API_DATABASE_URL", "API_LISTEN_ADDRESS", ":8080")
	if err != nil {
		return API{}, err
	}
	polling, err := integerRange("UI_POLLING_INTERVAL_SECONDS", 30, minPollingSeconds, maxPollingSeconds)
	if err != nil {
		return API{}, err
	}
	padding, err := integerRange("MAP_FIT_PADDING_PIXELS", 48, 0, maxMapPaddingPixels)
	if err != nil {
		return API{}, err
	}
	baseMaps, err := parseBaseMaps(os.Getenv("BASE_MAPS_JSON"))
	if err != nil {
		return API{}, err
	}
	passwordMinimum, err := integerRange("PASSWORD_MINIMUM_LENGTH", 12, 12, 64)
	if err != nil {
		return API{}, err
	}
	pageMaximum, err := integerRange("PAGE_SIZE_MAXIMUM", 100, 25, 1000)
	if err != nil {
		return API{}, err
	}
	rateKey, err := secretKey("RATE_LIMIT_KEY")
	if err != nil {
		return API{}, err
	}
	trustedProxies, err := cidrs("TRUSTED_PROXY_CIDRS")
	if err != nil {
		return API{}, err
	}
	allowInsecureSMTP, err := boolean("SMTP_ALLOW_INSECURE_LOCAL", false)
	if err != nil {
		return API{}, err
	}
	localDevelopment, err := boolean("LOCAL_DEVELOPMENT", false)
	if err != nil {
		return API{}, err
	}
	sessionLifetime, err := durationRange("SESSION_LIFETIME", 2*time.Hour, 5*time.Minute, 24*time.Hour)
	if err != nil {
		return API{}, err
	}
	result := API{
		Common:                 c,
		PublicURL:              env("PUBLIC_URL", "http://localhost:5173"),
		PollingIntervalSeconds: polling,
		MapFitPaddingPixels:    padding,
		BaseMaps:               baseMaps,
		PGTileServURL:          env("PG_TILESERV_URL", "http://127.0.0.1:7800"),
		SessionLifetime:        sessionLifetime,
		PasswordMinimum:        passwordMinimum,
		PageSizeMaximum:        pageMaximum,
		RateLimitKey:           rateKey,
		TrustedProxyCIDRs:      trustedProxies,
		LocalDevelopment:       localDevelopment,
		SMTP: SMTP{
			Address:            os.Getenv("SMTP_ADDRESS"),
			Username:           os.Getenv("SMTP_USERNAME"),
			PasswordFile:       os.Getenv("SMTP_PASSWORD_FILE"),
			FromAddress:        os.Getenv("SMTP_FROM_ADDRESS"),
			AllowInsecureLocal: allowInsecureSMTP,
		},
	}
	if err := validatePublicOrigin(result.PublicURL, result.LocalDevelopment); err != nil {
		return API{}, err
	}
	if err := validateHTTPURL("PG_TILESERV_URL", result.PGTileServURL, false); err != nil {
		return API{}, err
	}
	if err := validateSMTP(result.SMTP, result.LocalDevelopment); err != nil {
		return API{}, err
	}
	return result, nil
}

func secretKey(name string) ([]byte, error) {
	raw := os.Getenv(name)
	decoded, err := base64.RawStdEncoding.DecodeString(raw)
	if err != nil || len(decoded) < 32 {
		return nil, fmt.Errorf("%s must be unpadded base64 encoding at least 32 random bytes", name)
	}
	return decoded, nil
}

func cidrs(name string) ([]*net.IPNet, error) {
	if strings.TrimSpace(os.Getenv(name)) == "" {
		return nil, nil
	}
	parts := strings.Split(os.Getenv(name), ",")
	result := make([]*net.IPNet, 0, len(parts))
	for _, raw := range parts {
		_, network, err := net.ParseCIDR(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("%s must contain only CIDR prefixes", name)
		}
		result = append(result, network)
	}
	return result, nil
}

func validateSMTP(cfg SMTP, localDevelopment bool) error {
	host, port, err := net.SplitHostPort(cfg.Address)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("SMTP_ADDRESS must be a host and port")
	}
	if cfg.FromAddress == "" {
		return fmt.Errorf("SMTP_FROM_ADDRESS is required")
	}
	from, err := mail.ParseAddress(cfg.FromAddress)
	if err != nil || from.Address != cfg.FromAddress || !strings.Contains(cfg.FromAddress, "@") {
		return fmt.Errorf("SMTP_FROM_ADDRESS must be one addr-spec without a display name")
	}
	if cfg.AllowInsecureLocal {
		mailpitHost := strings.EqualFold(host, "mailpit") || strings.HasPrefix(strings.ToLower(host), "mailpit.")
		if !localDevelopment || port != "1025" || (!isLoopbackHost(host) && !mailpitHost) || cfg.Username != "" || cfg.PasswordFile != "" {
			return fmt.Errorf("SMTP_ALLOW_INSECURE_LOCAL requires LOCAL_DEVELOPMENT and a loopback or Mailpit host on port 1025 without credentials")
		}
		return nil
	}
	if cfg.Username == "" || cfg.PasswordFile == "" {
		return fmt.Errorf("SMTP_USERNAME and SMTP_PASSWORD_FILE are required")
	}
	return nil
}

func validatePublicOrigin(raw string, localDevelopment bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("PUBLIC_URL must be an origin without user information, path, query, or fragment")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme != "http" || !localDevelopment || !isLoopbackHost(parsed.Hostname()) {
		return fmt.Errorf("PUBLIC_URL must use https except for explicit loopback local development")
	}
	return nil
}

func LoadWorker() (Worker, error) {
	common, err := loadCommon("WORKER_DATABASE_URL", "WORKER_LISTEN_ADDRESS", ":8081")
	if err != nil {
		return Worker{}, err
	}
	osmDatabaseURL := os.Getenv("OSM_DATABASE_URL")
	if osmDatabaseURL == "" {
		return Worker{}, fmt.Errorf("OSM_DATABASE_URL is required")
	}
	fileConcurrency, err := integerRange("WORKER_FILE_CONCURRENCY", 2, 1, 16)
	if err != nil {
		return Worker{}, err
	}
	accountConcurrency, err := integerRange("ACCOUNT_FILE_CONCURRENCY", 2, 1, 16)
	if err != nil {
		return Worker{}, err
	}
	globalConcurrency, err := integerRange("GLOBAL_FILE_CONCURRENCY", 4, 1, 16)
	if err != nil {
		return Worker{}, err
	}
	if accountConcurrency > globalConcurrency {
		return Worker{}, fmt.Errorf("ACCOUNT_FILE_CONCURRENCY must not exceed GLOBAL_FILE_CONCURRENCY")
	}
	autoSyncInterval, err := durationRange("AUTO_SYNC_INTERVAL", 24*time.Hour, 5*time.Minute, 168*time.Hour)
	if err != nil {
		return Worker{}, err
	}
	autoSyncPollInterval, err := durationRange("AUTO_SYNC_POLL_INTERVAL", 30*time.Second, time.Second, 5*time.Minute)
	if err != nil {
		return Worker{}, err
	}
	autoSyncStaleDays, err := integerRange("AUTO_SYNC_STALE_DAYS", 3, 1, 30)
	if err != nil {
		return Worker{}, err
	}
	schedulerLease, err := durationRange("SCHEDULER_LEASE_DURATION", 2*time.Minute, time.Second, 15*time.Minute)
	if err != nil {
		return Worker{}, err
	}
	if schedulerLease <= autoSyncPollInterval {
		return Worker{}, fmt.Errorf("SCHEDULER_LEASE_DURATION must exceed AUTO_SYNC_POLL_INTERVAL")
	}
	osm, err := loadOSM()
	if err != nil {
		return Worker{}, err
	}
	stagingRoot := env("WORKER_STAGING_ROOT", "/var/lib/workouts/staging")
	if !filepath.IsAbs(stagingRoot) || filepath.Clean(stagingRoot) != stagingRoot || stagingRoot == string(filepath.Separator) {
		return Worker{}, fmt.Errorf("WORKER_STAGING_ROOT must be a clean absolute path other than the filesystem root")
	}
	return Worker{
		Common: common, OSMDatabaseURL: osmDatabaseURL, FileConcurrency: fileConcurrency, AccountConcurrency: accountConcurrency,
		GlobalConcurrency: globalConcurrency, StagingRoot: stagingRoot, AutoSyncInterval: autoSyncInterval,
		AutoSyncPollInterval: autoSyncPollInterval, AutoSyncStaleDays: autoSyncStaleDays, SchedulerLease: schedulerLease,
		OSM: osm,
	}, nil
}

func loadOSM() (OSM, error) {
	autoAdd, err := boolean("OSM_AUTO_ADD_REGIONS", false)
	if err != nil {
		return OSM{}, err
	}
	maximum, err := int64Range("OSM_MAX_AUTO_DOWNLOAD_BYTES", 1<<30, 1, 1<<40)
	if err != nil {
		return OSM{}, err
	}
	providers, err := identifierList("OSM_DATA_PROVIDERS_JSON", `["geofabrik"]`, false)
	if err != nil {
		return OSM{}, err
	}
	regions, err := identifierList("OSM_REGIONS_JSON", `["geofabrik:norcal"]`, true)
	if err != nil {
		return OSM{}, err
	}
	if autoAdd && len(providers) == 0 {
		return OSM{}, fmt.Errorf("OSM_DATA_PROVIDERS_JSON must not be empty when OSM_AUTO_ADD_REGIONS is enabled")
	}
	return OSM{AutoAddRegions: autoAdd, MaxAutoDownloadBytes: maximum, DataProviders: providers, Regions: regions}, nil
}

func identifierList(name, fallback string, qualified bool) ([]string, error) {
	raw := os.Getenv(name)
	if strings.TrimSpace(raw) == "" {
		raw = fallback
	}
	var values []string
	decoder := json.NewDecoder(strings.NewReader(raw))
	if err := decoder.Decode(&values); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("%s must be one JSON string array", name)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		parts := strings.Split(value, ":")
		if (!qualified && len(parts) != 1) || (qualified && len(parts) != 2) {
			return nil, fmt.Errorf("%s contains an invalid identifier", name)
		}
		for _, part := range parts {
			if !validIdentifier(part) {
				return nil, fmt.Errorf("%s contains an invalid identifier", name)
			}
		}
		if _, exists := seen[value]; exists {
			return nil, fmt.Errorf("%s must not contain duplicates", name)
		}
		seen[value] = struct{}{}
	}
	return values, nil
}

func validIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, character := range value[1:] {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func loadCommon(databaseVariable, listenVariable, defaultListen string) (Common, error) {
	databaseURL := os.Getenv(databaseVariable)
	if databaseURL == "" {
		return Common{}, fmt.Errorf("%s is required", databaseVariable)
	}
	keyringFile := os.Getenv("SOURCE_KEYRING_FILE")
	if keyringFile == "" || !filepath.IsAbs(keyringFile) {
		return Common{}, fmt.Errorf("SOURCE_KEYRING_FILE must be an absolute path")
	}
	roots, err := sourceconfig.ParseRoots(os.Getenv("LOCAL_SOURCE_ROOTS"))
	if err != nil {
		return Common{}, err
	}
	return Common{
		DatabaseURL:       databaseURL,
		ListenAddress:     env(listenVariable, defaultListen),
		OTLPEndpoint:      os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
		SourceKeyringFile: keyringFile,
		LocalSourceRoots:  roots,
	}, nil
}

func ShutdownTimeout() time.Duration { return 10 * time.Second }

func ReadHeaderTimeout() time.Duration { return 5 * time.Second }
func ReadTimeout() time.Duration       { return 15 * time.Second }
func WriteTimeout() time.Duration      { return 30 * time.Second }
func IdleTimeout() time.Duration       { return 60 * time.Second }

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func integerRange(name string, fallback, minimum, maximum int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", name, minimum, maximum)
	}
	return value, nil
}

func int64Range(name string, fallback, minimum, maximum int64) (int64, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be an integer from %d through %d", name, minimum, maximum)
	}
	return value, nil
}

func durationRange(name string, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be a duration between %s and %s", name, minimum, maximum)
	}
	return value, nil
}

func boolean(name string, fallback bool) (bool, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return value, nil
}

func validateHTTPURL(name, raw string, allowFragment bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return fmt.Errorf("%s must be an http or https URL without user information", name)
	}
	if !allowFragment && parsed.Fragment != "" {
		return fmt.Errorf("%s must not contain a fragment", name)
	}
	if parsed.RawQuery != "" {
		return fmt.Errorf("%s must not contain a query", name)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
