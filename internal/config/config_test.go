package config

import (
	"strings"
	"testing"
)

func TestLoadAPIValidatesPublicConfig(t *testing.T) {
	t.Setenv("API_DATABASE_URL", "postgresql://database.invalid/workouts")
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

func TestLoadAPIAllowsExplicitLocalMapOnlyForLocalDevelopment(t *testing.T) {
	t.Setenv("API_DATABASE_URL", "postgresql://database.invalid/workouts")
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
	t.Setenv("PUBLIC_URL", "https://workouts.example.com")
	t.Setenv("BASE_MAP_TILE_URL", "https://tiles.example.com/{z}/{x}/{y}.png")
	if _, err := LoadAPI(); err != nil {
		t.Fatal(err)
	}
}
