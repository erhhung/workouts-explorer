package rclonesource

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
)

func TestStderrBoundCannotBeBypassedByCopy(t *testing.T) {
	var stderr privateStderr
	if _, err := io.CopyN(&stderr, endlessByte{}, 128<<10); err != nil {
		t.Fatal(err)
	}
	if stderr.buffer.Len() != 64<<10 {
		t.Fatal("stderr exceeded its hard cap")
	}
}

func TestBoundedWriter(t *testing.T) {
	var output bytes.Buffer
	kills := 0
	writer := &boundedWriter{dst: &output, remaining: 5, kill: func() { kills++ }}
	if n, err := writer.Write([]byte("hello!")); n != 5 || !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("stream not stopped at cap")
	}
	if kills != 1 || output.String() != "hello" || !writer.exceeded {
		t.Fatal("stream cap not enforced")
	}
	writer = &boundedWriter{dst: failedWriter{}, remaining: 10, kill: func() { kills++ }}
	if _, err := writer.Write([]byte("hello")); !errors.Is(err, io.ErrShortWrite) || !writer.failed || kills != 2 {
		t.Fatal("writer failure did not kill child")
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, errors.New(contentCanary) }

func TestSafeContextCause(t *testing.T) {
	err := failure("source-unavailable", fmt.Errorf("%s: %w", contentCanary, context.Canceled))
	assertCode(t, err, "source-unavailable")
	if errors.Unwrap(err) != context.Canceled {
		t.Fatal("private wrapped cause escaped")
	}
	err = failure("source-unavailable", errors.New(contentCanary))
	if errors.Unwrap(err) != nil {
		t.Fatal("private cause escaped")
	}
}

func TestChildStartupFailureIsSanitized(t *testing.T) {
	s := newTestSource(t)
	s.binary = s.dir + "/" + contentCanary
	assertCode(t, s.Check(context.Background()), "source-unavailable")
	if s.Config() != testConfig() {
		t.Fatal("startup failure changed config")
	}
}

func TestPCSFailureSignatures(t *testing.T) {
	for _, test := range []struct{ name, stderr, code string }{
		{"500", `requestPCS(iclouddrive): HTTP error 500 (500 Internal Server Error) returned body: "PRIVATE-COOKIE-CANARY"`, "source-unavailable"},
		{"429", `requestPCS(iclouddrive): HTTP error 429 (429 Too Many Requests) returned body: "PRIVATE-COOKIE-CANARY"`, "source-unavailable"},
		{"dns", `requestPCS(iclouddrive): Post "https://private.invalid/requestPCS": dial tcp: lookup private.invalid: no such host`, "source-unavailable"},
		{"network-timeout", `requestPCS(iclouddrive): Post "https://private.invalid/requestPCS": net/http: TLS handshake timeout`, "source-unavailable"},
		{"context-timeout", "requestPCS(iclouddrive): context deadline exceeded", "source-unavailable"},
		{"generic-423", `requestPCS(iclouddrive): HTTP error 423 (423 Locked) returned body: "PRIVATE-COOKIE-CANARY"`, "source-unavailable"},
		{"unrelated-missing-text", `requestPCS(iclouddrive): HTTP error 500 (500 Internal Server Error) returned body: "Missing PCS cookies from the request"`, "source-unavailable"},
		{"missing-cookies", "requestPCS(iclouddrive): Missing PCS cookies from the request", "source-adp-approval-required"},
		{"missing-423", `requestPCS(iclouddrive): HTTP error 423 (423 Locked) returned body: "{\"reason\":\"Missing PCS cookies from the request\",\"private\":\"PRIVATE-COOKIE-CANARY\"}"`, "source-adp-approval-required"},
		{"missing-after-success", "requestPCS(iclouddrive): server returned success but cookies still missing: [X-APPLE-WEBAUTH-PCS-Documents]", "source-adp-approval-required"},
		{"approval-timeout", "requestPCS(iclouddrive): timed out waiting for device approval after 5 minutes", "source-adp-approval-required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, fallback := range []string{"source-unavailable", "source-file-unavailable"} {
				for _, prefix := range []string{"", `2026/01/01 00:00:00 CRITICAL: Failed to create file system for "source:exports": `} {
					code := classify(prefix+test.stderr, fallback)
					if code != test.code {
						t.Fatalf("classification = %s, want %s", code, test.code)
					}
					assertCode(t, failure(code, nil), code)
				}
			}
		})
	}
}
