package api

import (
	"context"
	"crypto/md5" // Gravatar's public protocol requires MD5 of the canonical email.
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"
)

type avatarEntry struct {
	contentType  string
	body         []byte
	etag         string
	expires      time.Time
	cacheControl string
}

type avatarService struct {
	client *http.Client
}

func newAvatarService() *avatarService {
	return &avatarService{
		client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

func (s *avatarService) get(ctx context.Context, canonicalEmail, fullName string) avatarEntry {
	// Browser caching remains identity-scoped. Do not retain identity-keyed
	// private images in API memory after an account purge.
	digest := md5.Sum([]byte(canonicalEmail)) //nolint:gosec // Required solely as a non-security Gravatar lookup identifier.
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://secure.gravatar.com/avatar/"+hex.EncodeToString(digest[:])+"?d=404&s=160", nil)
	request.Header.Set("User-Agent", "Workouts-Explorer-Avatar-Proxy/1")
	response, err := s.client.Do(request)
	if err == nil {
		defer response.Body.Close()
		contentType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
		if response.StatusCode == http.StatusOK && (contentType == "image/png" || contentType == "image/jpeg" || contentType == "image/webp") {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
			if readErr == nil && len(body) > 0 && len(body) <= 1<<20 {
				return makeAvatarEntry(contentType, body, 24*time.Hour, "private, max-age=3600")
			}
		}
	}
	return fallbackAvatar(fullName)
}

func fallbackAvatar(fullName string) avatarEntry {
	initial := "?"
	if trimmed := strings.TrimSpace(fullName); trimmed != "" {
		initial = strings.ToUpper(string([]rune(trimmed)[0]))
	}
	body := []byte(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 160 160" role="img"><rect width="160" height="160" rx="80" fill="#174f46"/><text x="80" y="112" text-anchor="middle" font-family="sans-serif" font-size="72" fill="#f7f1df">%s</text></svg>`, html.EscapeString(initial)))
	return makeAvatarEntry("image/svg+xml", body, 30*time.Second, "private, max-age=30, must-revalidate")
}

func makeAvatarEntry(contentType string, body []byte, ttl time.Duration, cacheControl string) avatarEntry {
	digest := sha256.Sum256(body)
	return avatarEntry{contentType: contentType, body: body, etag: `"` + hex.EncodeToString(digest[:]) + `"`, expires: time.Now().Add(ttl), cacheControl: cacheControl}
}
