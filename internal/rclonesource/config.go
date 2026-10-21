package rclonesource

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"golang.org/x/sys/unix"
)

const maxConfig = 1 << 20

func iniValue(value string) bool {
	return utf8.ValidString(value) && len(value) <= maxConfig/4 && value == strings.TrimSpace(value) &&
		!strings.ContainsFunc(value, unicode.IsControl) && !strings.Contains(value, "`") &&
		!strings.Contains(value, `"""`) && !strings.Contains(value, "%(")
}

func validCookies(value string) bool {
	if value == "" || !iniValue(value) {
		return false
	}
	cookies, err := http.ParseCookie(value)
	if err != nil || len(cookies) == 0 {
		return false
	}
	for _, cookie := range cookies {
		if cookie.Value == "" || cookie.Valid() != nil {
			return false
		}
	}
	return true
}

func validConfig(config sourceconfig.ICloud) bool {
	if _, _, err := sourceconfig.CanonicalizeICloud(config); err != nil || !validCookies(config.Cookies) {
		return false
	}
	for _, value := range []string{config.AppleID, config.Password, config.TrustToken, config.ClientID, config.Path} {
		if !iniValue(value) {
			return false
		}
	}
	return true
}

func configValues(config sourceconfig.ICloud) map[string]string {
	values := map[string]string{"type": "iclouddrive", "service": "drive", "apple_id": config.AppleID,
		"password": config.Password, "cookies": config.Cookies, "trust_token": config.TrustToken}
	if config.ClientID != "" {
		values["client_id"] = config.ClientID
	}
	return values
}

func (s *Source) writeConfig() error {
	// Atomic replacement restores the trusted one-profile file after every
	// process, including a failed or interrupted refresh. Never follow a link.
	f, err := s.root.OpenFile("config.new", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return failure("source-config-invalid", nil)
	}
	defer func() { _ = f.Close(); _ = s.root.Remove("config.new") }()
	if _, err = io.WriteString(f, "[source]\n"); err != nil {
		return failure("source-config-invalid", nil)
	}
	values := configValues(s.config)
	for _, key := range []string{"type", "service", "apple_id", "password", "cookies", "trust_token", "client_id"} {
		if value, ok := values[key]; ok {
			if _, err = fmt.Fprintf(f, "%s = %s\n", key, value); err != nil {
				return failure("source-config-invalid", nil)
			}
		}
	}
	if err = f.Close(); err != nil {
		return failure("source-config-invalid", nil)
	}
	if err = s.root.Rename("config.new", configName); err != nil {
		return failure("source-config-invalid", nil)
	}
	return nil
}

// parseConfig implements only the closed INI subset this adapter writes, plus
// rclone's empty _auth_session. No includes, extra sections, duplicate keys,
// substitutions, multiline values, descriptions or arbitrary backend flags.
func parseConfig(reader io.Reader) (map[string]string, bool) {
	data, err := io.ReadAll(io.LimitReader(reader, maxConfig+1))
	if err != nil || len(data) > maxConfig || !utf8.Valid(data) {
		return nil, false
	}
	values := make(map[string]string)
	section := false
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 4096), maxConfig)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.ContainsFunc(line, unicode.IsControl) {
			return nil, false
		}
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if line == "[source]" && !section {
			section = true
			continue
		}
		if !section {
			return nil, false
		}
		key, value, ok := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok {
			return nil, false
		}
		if _, duplicate := values[key]; duplicate {
			return nil, false
		}
		switch key {
		case "type", "service", "apple_id", "password", "cookies", "trust_token", "client_id":
			if !iniValue(value) {
				return nil, false
			}
		case "_auth_session":
			if value != "" && value != "''" {
				return nil, false
			}
		default:
			return nil, false
		}
		values[key] = value
	}
	return values, section && scanner.Err() == nil
}

func (s *Source) harvest() bool {
	f, err := s.root.OpenFile(configName, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode() != 0600 || info.Size() > maxConfig {
		return false
	}
	values, ok := parseConfig(f)
	if !ok {
		return false
	}
	for key, value := range configValues(s.config) {
		if key != "cookies" && values[key] != value {
			return false
		}
		if _, present := values[key]; !present {
			return false
		}
	}
	if s.config.ClientID == "" && values["client_id"] != "" {
		return false
	}
	candidate := s.config
	candidate.Cookies = values["cookies"]
	if !validConfig(candidate) {
		return false
	}
	s.config = candidate
	return true
}
