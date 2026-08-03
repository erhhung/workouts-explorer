package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	MaxRequestBodyBytes int64 = 1 << 20
	minPollingSeconds         = 1
	maxPollingSeconds         = 3600
	maxMapPaddingPixels       = 512
)

type Common struct {
	DatabaseURL   string
	ListenAddress string
	OTLPEndpoint  string
}

type API struct {
	Common
	PublicURL              string
	PollingIntervalSeconds int
	MapFitPaddingPixels    int
	BaseMapTileURL         string
	BaseMapAttribution     string
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
	result := API{
		Common:                 c,
		PublicURL:              env("PUBLIC_URL", "http://localhost:5173"),
		PollingIntervalSeconds: polling,
		MapFitPaddingPixels:    padding,
		BaseMapTileURL:         os.Getenv("BASE_MAP_TILE_URL"),
		BaseMapAttribution:     os.Getenv("BASE_MAP_ATTRIBUTION"),
	}
	if err := validateHTTPURL("PUBLIC_URL", result.PublicURL, false); err != nil {
		return API{}, err
	}
	allowLocalMap, err := boolean("ALLOW_LOCAL_BASE_MAP", false)
	if err != nil {
		return API{}, err
	}
	if result.BaseMapTileURL != "" {
		if err := validateBaseMapURL(result.BaseMapTileURL, result.PublicURL, allowLocalMap); err != nil {
			return API{}, err
		}
	}
	if len(result.BaseMapAttribution) > 1024 {
		return API{}, fmt.Errorf("BASE_MAP_ATTRIBUTION must not exceed 1024 characters")
	}
	return result, nil
}

func LoadWorker() (Common, error) {
	return loadCommon("WORKER_DATABASE_URL", "WORKER_LISTEN_ADDRESS", ":8081")
}

func loadCommon(databaseVariable, listenVariable, defaultListen string) (Common, error) {
	databaseURL := os.Getenv(databaseVariable)
	if databaseURL == "" {
		return Common{}, fmt.Errorf("%s is required", databaseVariable)
	}
	return Common{
		DatabaseURL:   databaseURL,
		ListenAddress: env(listenVariable, defaultListen),
		OTLPEndpoint:  os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
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

func validateBaseMapURL(raw, publicURL string, allowLocal bool) error {
	if err := validateHTTPURL("BASE_MAP_TILE_URL", raw, false); err != nil {
		return err
	}
	parsed, _ := url.Parse(raw)
	if !isInternalHost(parsed.Hostname()) {
		return nil
	}
	public, _ := url.Parse(publicURL)
	if !allowLocal || !isInternalHost(public.Hostname()) {
		return fmt.Errorf("BASE_MAP_TILE_URL must not use a loopback, private, .local, or .internal host")
	}
	return nil
}

func validateHTTPURL(name, raw string, allowFragment bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return fmt.Errorf("%s must be an http or https URL without user information", name)
	}
	if !allowFragment && parsed.Fragment != "" {
		return fmt.Errorf("%s must not contain a fragment", name)
	}
	return nil
}

func isInternalHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified())
}
