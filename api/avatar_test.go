package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestAvatarProxyPrivacyCachingAndSizeLimit(t *testing.T) {
	requests := 0
	service := newAvatarService()
	service.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.Scheme != "https" || request.URL.Host != "secure.gravatar.com" || strings.Contains(request.URL.String(), "owner@example.test") || request.URL.Query().Get("d") != "404" {
			t.Fatalf("unsafe Gravatar request %q", request.URL.String())
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"image/png"}}, Body: io.NopCloser(strings.NewReader("png-data")), Request: request}, nil
	})
	first := service.get(context.Background(), "owner@example.test", "Owner")
	second := service.get(context.Background(), "owner@example.test", "Owner")
	if first.contentType != "image/png" || second.etag != first.etag || requests != 1 {
		t.Fatalf("avatar cache content=%q requests=%d", first.contentType, requests)
	}

	service = newAvatarService()
	service.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"image/png"}}, Body: io.NopCloser(io.LimitReader(&infiniteReader{}, 1<<20+1)), Request: request}, nil
	})
	oversized := service.get(context.Background(), "large@example.test", "<Owner>")
	if oversized.contentType != "image/svg+xml" || len(oversized.body) > 1024 || strings.Contains(string(oversized.body), "<Owner>") {
		t.Fatal("oversized avatar did not use a safe bounded fallback")
	}
}

func TestAvatarProxyDoesNotFollowRedirects(t *testing.T) {
	requests := 0
	service := newAvatarService()
	service.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"http://attacker.invalid/avatar"}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})
	entry := service.get(context.Background(), "redirect@example.test", "R")
	if requests != 1 || entry.contentType != "image/svg+xml" {
		t.Fatalf("redirect requests=%d content=%q", requests, entry.contentType)
	}
}

type infiniteReader struct{}

func (*infiniteReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 'x'
	}
	return len(buffer), nil
}
