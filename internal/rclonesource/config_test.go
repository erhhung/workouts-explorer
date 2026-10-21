package rclonesource

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCookieHarvestOnly(t *testing.T) {
	for _, test := range []struct {
		scenario, code string
		refresh        bool
	}{
		{"refresh", "", true}, {"refresh-fail", "source-unavailable", true},
		{"refresh-auth-fail", "source-auth-failed", false}, {"auth-session", "", true},
		{"empty-cookies", "source-config-invalid", false}, {"change-trust", "source-config-invalid", false},
		{"extra-section", "source-config-invalid", false},
	} {
		t.Run(test.scenario, func(t *testing.T) {
			s := newTestSource(t)
			t.Setenv(helperScenario, test.scenario)
			err := s.Check(context.Background())
			if test.code != "" {
				assertCode(t, err, test.code)
			} else if err != nil {
				t.Fatal(err)
			}
			expected := testConfig()
			if test.refresh {
				expected.Cookies = "sid=REFRESHED-COOKIE-CANARY"
			}
			if s.Config() != expected {
				t.Fatal("harvest changed immutable fields or accepted corrupt cookies")
			}
			data, err := s.root.ReadFile(configName)
			if err != nil {
				t.Fatal(err)
			}
			values, ok := parseConfig(bytes.NewReader(data))
			if !ok || values["trust_token"] != trustCanary || values["cookies"] != expected.Cookies {
				t.Fatal("private config was not restored")
			}
			if _, exists := values["_auth_session"]; exists {
				t.Fatal("auth session was persisted")
			}
			t.Setenv(helperScenario, "")
			if err := s.Check(context.Background()); err != nil {
				t.Fatal("subsequent child read corrupt config")
			}
		})
	}
}

func TestClosedINIParser(t *testing.T) {
	valid := "[source]\ntype = iclouddrive\nservice = drive\napple_id = account\npassword = obscured\ncookies = sid=value; other=value\ntrust_token = trusted\n"
	for _, addition := range []string{
		"\n[source]\n", "\n[other]\n", "type = local\n", "description = private\n",
		"encoding = None\n", "_auth_session = nonempty\n", "unknown = value\n",
		"client_id = %(trust_token)s\n",
		"bad-line\n", "cookies = \n", "[source] trailing\n", "#\x00private\n",
	} {
		_, ok := parseConfig(strings.NewReader(valid + addition))
		if ok {
			t.Fatalf("closed parser accepted %q", addition)
		}
	}
	for _, data := range []string{"type = local\n" + valid, "[unknown]\n" + valid, strings.Repeat("x", maxConfig+1)} {
		if _, ok := parseConfig(strings.NewReader(data)); ok {
			t.Fatal("unsafe INI accepted")
		}
	}
	for _, addition := range []string{"", "_auth_session = \n", "_auth_session = ''\n", "# comment\n; comment\n"} {
		values, ok := parseConfig(strings.NewReader(valid + addition))
		if !ok || values["cookies"] != "sid=value; other=value" {
			t.Fatal("valid INI rejected")
		}
	}
}

func TestConfigValidation(t *testing.T) {
	if !validConfig(testConfig()) {
		t.Fatal("valid config rejected")
	}
	for _, change := range []func(*Source){
		func(s *Source) { s.config.AppleID += "\n[evil]" },
		func(s *Source) { s.config.Password = "plaintext-password" },
		func(s *Source) { s.config.Cookies = "" },
		func(s *Source) { s.config.Cookies = "sid=" },
		func(s *Source) { s.config.Cookies = "not-a-cookie" },
		func(s *Source) { s.config.TrustToken = "" },
		func(s *Source) { s.config.Path = "../private" },
		func(s *Source) { s.config.Path = "/private" },
		func(s *Source) { s.config.Path = "other:private" },
		func(s *Source) { s.config.Path = "exports`private" },
		func(s *Source) { s.config.Version = 2 },
		func(s *Source) { s.config.ClientID = "bad client" },
		func(s *Source) { s.config.TrustToken = "%(password)s" },
	} {
		s := &Source{config: testConfig()}
		change(s)
		if validConfig(s.config) {
			t.Fatal("unsafe config accepted")
		}
	}
}

func TestHarvestRejectsSymlinkAndMode(t *testing.T) {
	s := newTestSource(t)
	original := s.Config()
	file := s.dir + "/" + configName
	if err := os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	if s.harvest() || s.Config() != original {
		t.Fatal("public config was harvested")
	}
	if err := s.writeConfig(); err != nil {
		t.Fatal(err)
	}
	if err := s.root.Remove(configName); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir() + "/canary"
	if err := os.WriteFile(external, []byte(contentCanary), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, file); err != nil {
		t.Fatal(err)
	}
	if s.harvest() || s.Config() != original {
		t.Fatal("linked config was harvested")
	}
	if err := s.writeConfig(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(external)
	if err != nil || string(data) != contentCanary {
		t.Fatal("config rewrite followed symlink")
	}
}

func TestHarvestRejectsFIFOWithoutBlocking(t *testing.T) {
	s := newTestSource(t)
	if err := s.root.Remove(configName); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(s.dir+"/"+configName, 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if s.harvest() {
		t.Fatal("FIFO harvested as config")
	}
	if time.Since(start) > time.Second {
		t.Fatal("FIFO blocked config harvest")
	}
	if err := s.writeConfig(); err != nil {
		t.Fatal(err)
	}
}

func TestAnchoredAuthClassification(t *testing.T) {
	for _, test := range []struct{ stderr, code string }{
		{"trust token expired, please reauth", "source-auth-expired"},
		{"2026/01/01 00:00:00 ERROR : HealthAutoExport-2026-01-01.json: Failed to open: trust token expired, please reauth", "source-auth-expired"},
		{`2026/01/01 00:00:00 CRITICAL: Failed to create file system for "source:exports": trust token expired, please reauth`, "source-auth-expired"},
		{"authSRPComplete: sign in failed: incorrect username or password", "source-auth-failed"},
		{"authSRPComplete: sign in failed: 401 Unauthorized: PRIVATE-CONTENT-CANARY", "source-auth-failed"},
		{`authSRPInit: HTTP error 401 (401 Unauthorized) returned body: "PRIVATE-CONTENT-CANARY"`, "source-auth-failed"},
		{"Missing PCS cookies from the request", "source-adp-approval-required"},
		{`HTTP error 423 (423 Locked) returned body: "{\"reason\":\"Missing PCS cookies from the request\",\"private\":\"PRIVATE-COOKIE-CANARY\"}"`, "source-adp-approval-required"},
		{`HTTP error 423 (423 Locked) returned body: "Missing PCS cookies from the request"`, "source-adp-approval-required"},
		{"requestPCS(iclouddrive): HTTP error 423: PRIVATE-COOKIE-CANARY", "source-unavailable"},
		{"requestPCS(iclouddrive): Missing PCS cookies from the request", "source-adp-approval-required"},
		{`requestPCS(iclouddrive): HTTP error 423 (423 Locked) returned body: "{\"reason\":\"Missing PCS cookies from the request\",\"private\":\"PRIVATE-COOKIE-CANARY\"}"`, "source-adp-approval-required"},
		{"requestPCS(iclouddrive): timed out waiting for device approval after 5 minutes", "source-adp-approval-required"},
		{"requestPCS(iclouddrive): server returned success but cookies still missing: [X-APPLE-WEBAUTH-PCS-Documents]", "source-adp-approval-required"},
		{"HTTP error 401 Unauthorized", "source-unavailable"},
		{"HTTP error 423 Locked", "source-unavailable"},
		{`HTTP error 423 (423 Locked) returned body: "{\"reason\":\"Temporary failure\"}"`, "source-unavailable"},
		{`HTTP error 423 (423 Locked) returned body: "PRIVATE-COOKIE-CANARY"`, "source-unavailable"},
		{"HTTP error 500 trust token expired, please reauth", "source-unavailable"},
		{`{"message":"trust token expired, please reauth"}`, "source-unavailable"},
		{"https://private.invalid/requestPCS", "source-unavailable"},
	} {
		code := classify(test.stderr, "source-unavailable")
		if code != test.code {
			t.Fatalf("classification = %s, want %s", code, test.code)
		}
		assertCode(t, failure(code, nil), code)
	}
}
