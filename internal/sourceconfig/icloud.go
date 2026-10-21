package sourceconfig

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const ICloudType = "health-auto-export-icloud"

// ICloud contains externally authenticated rclone credentials. Never serialize it in responses.
type ICloud struct {
	Version    int    `json:"version"`
	Path       string `json:"path"`
	AppleID    string `json:"appleId"`
	Password   string `json:"password"`
	Cookies    string `json:"cookies"`
	TrustToken string `json:"trustToken"`
	ClientID   string `json:"clientId,omitempty"`
}

var clientIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

var errICloudConfig = errors.New("invalid iCloud source configuration")

// Leave room for the GCM authentication tag inside sourcecrypto's 64 KiB ciphertext bound.
const maxConfigPlaintext = (64 << 10) - 16

func CanonicalizeICloud(config ICloud) (ICloud, []byte, error) {
	if config.Version != 1 || !validICloudValue(config.Path, 4096, false) ||
		!validICloudValue(config.AppleID, 320, false) || !validICloudValue(config.Password, 4096, false) ||
		!validICloudValue(config.Cookies, 32768, false) || !validICloudValue(config.TrustToken, 4096, false) ||
		!validICloudValue(config.ClientID, 128, true) {
		return ICloud{}, nil, errICloudConfig
	}
	if path.IsAbs(config.Path) || path.Clean(config.Path) != config.Path ||
		strings.ContainsAny(config.Path, `:\`) || strings.HasPrefix(config.Path, "~") {
		return ICloud{}, nil, errICloudConfig
	}
	for _, component := range strings.Split(config.Path, "/") {
		if component == "" || component == "." || component == ".." {
			return ICloud{}, nil, errICloudConfig
		}
	}
	// Rclone obscure is raw URL-base64 containing a 16-byte IV followed by ciphertext.
	obscured, err := base64.RawURLEncoding.DecodeString(config.Password)
	if err != nil || len(obscured) < 17 || base64.RawURLEncoding.EncodeToString(obscured) != config.Password ||
		(config.ClientID != "" && !clientIDPattern.MatchString(config.ClientID)) {
		return ICloud{}, nil, errICloudConfig
	}
	encoded, err := json.Marshal(config)
	if err != nil || len(encoded) > maxConfigPlaintext {
		return ICloud{}, nil, errICloudConfig
	}
	return config, encoded, nil
}

func DecodeICloud(encoded []byte) (ICloud, []byte, error) {
	return decodeICloud(encoded, ICloud{}, false)
}

// MergeICloud preserves omitted credentials; explicit null and required-field clearing fail closed.
func MergeICloud(current ICloud, patch []byte) (ICloud, []byte, error) {
	return decodeICloud(patch, current, true)
}

func decodeICloud(encoded []byte, config ICloud, partial bool) (ICloud, []byte, error) {
	if len(encoded) > 64<<10 || !utf8.Valid(encoded) {
		return ICloud{}, nil, errICloudConfig
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return ICloud{}, nil, errICloudConfig
	}
	seen := make(map[string]bool, 7)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return ICloud{}, nil, errICloudConfig
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return ICloud{}, nil, errICloudConfig
		}
		seen[key] = true
		var destination any
		switch key {
		case "version":
			destination = &config.Version
		case "path":
			destination = &config.Path
		case "appleId":
			destination = &config.AppleID
		case "password":
			destination = &config.Password
		case "cookies":
			destination = &config.Cookies
		case "trustToken":
			destination = &config.TrustToken
		case "clientId":
			destination = &config.ClientID
		default:
			return ICloud{}, nil, errICloudConfig
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, destination) != nil {
			return ICloud{}, nil, errICloudConfig
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return ICloud{}, nil, errICloudConfig
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || len(seen) == 0 {
		return ICloud{}, nil, errICloudConfig
	}
	if !partial {
		for _, key := range []string{"version", "path", "appleId", "password", "cookies", "trustToken"} {
			if !seen[key] {
				return ICloud{}, nil, errICloudConfig
			}
		}
	}
	return CanonicalizeICloud(config)
}

func validICloudValue(value string, limit int, optional bool) bool {
	if len(value) > limit || !utf8.ValidString(value) || (!optional && value == "") || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' || r == unicode.ReplacementChar {
			return false
		}
	}
	return true
}

// Canonicalize validates a closed adapter document without changing local path semantics.
func Canonicalize(sourceType string, encoded []byte, approvedRoots []string) ([]byte, error) {
	switch sourceType {
	case LocalType:
		_, canonical, err := DecodeLocal(encoded, approvedRoots)
		return canonical, err
	case ICloudType:
		_, canonical, err := DecodeICloud(encoded)
		return canonical, err
	default:
		return nil, errors.New("unsupported source type")
	}
}
