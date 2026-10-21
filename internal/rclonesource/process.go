package rclonesource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Error exposes only stable, sanitized fields. Unwrap returns a context sentinel
// or, for failed constructor cleanup, the sanitized constructor error. It never
// exposes stderr, filesystem errors, exec.ExitError, URLs, arguments or contents.
type Error struct {
	Code    string
	Summary string
	cause   error
}

func (e *Error) Error() string { return e.Code + ": " + e.Summary }
func (e *Error) Unwrap() error { return e.cause }

func failure(code string, cause error) *Error {
	var summary string
	switch code {
	case "source-auth-expired":
		summary = "Source authentication expired; reauthentication is required"
	case "source-auth-failed":
		summary = "Source authentication failed"
	case "source-adp-approval-required":
		summary = "Source requires trusted-device approval for Advanced Data Protection"
	case "source-file-unavailable":
		summary = "Source file is unavailable"
	case "source-file-mutated":
		summary = "Source file changed during ingestion"
	case "source-directory-limit":
		summary = "Source directory exceeds the ingestion limit"
	case "source-file-invalid":
		summary = "Source file metadata is invalid or exceeds the ingestion limit"
	case "source-config-invalid":
		summary = "Source configuration is invalid"
	case "source-cleanup-failed":
		summary = "Private source staging cleanup failed; job recovery is required"
	default:
		code = "source-unavailable"
		summary = "Source is unavailable"
	}
	if errors.Is(cause, context.Canceled) {
		cause = context.Canceled
	} else if errors.Is(cause, context.DeadlineExceeded) {
		cause = context.DeadlineExceeded
	} else {
		cause = nil
	}
	return &Error{Code: code, Summary: summary, cause: cause}
}

// A numeric HTTP status alone is not an auth diagnosis. These signatures come
// from the pinned backend's auth/PCS paths; arbitrary transient 401/423 errors
// and response bodies remain source-unavailable.
var authSignature = regexp.MustCompile(`^(?:authSRPComplete: )?sign in failed: (?:incorrect username or password|401 Unauthorized(?:: .*)?|403 Forbidden(?:: .*)?)$`)
var authHTTPSignature = regexp.MustCompile(`^(?:authStart|authFederate|authSRPInit): HTTP error 401 \(401 Unauthorized\) returned body: ".*"$`)
var logPrefix = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} (?:ERROR\s*:|CRITICAL:)\s*`)
var operationPrefix = regexp.MustCompile(`^(?:Failed to create file system for "[^"\r\n]*": |Failed to lsjson: |error in ListJSON: |creating entry failed in ListJSON: |Failed to cat: |HealthAutoExport-\d{4}-\d{2}-\d{2}\.json: Failed to open: )`)

func classify(stderr string, fallback string) string {
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(logPrefix.ReplaceAllString(line, ""))
		for {
			rest := operationPrefix.ReplaceAllString(line, "")
			if rest == line {
				break
			}
			line = rest
		}
		if pcs, ok := strings.CutPrefix(line, "requestPCS(iclouddrive): "); ok {
			if pcs == "timed out waiting for device approval after 5 minutes" ||
				pcs == "server returned success but cookies still missing: [X-APPLE-WEBAUTH-PCS-Documents]" {
				return "source-adp-approval-required"
			}
			line = pcs
			fallback = "source-unavailable"
		}
		switch {
		case line == "trust token expired, please reauth":
			return "source-auth-expired"
		case authSignature.MatchString(line), authHTTPSignature.MatchString(line):
			return "source-auth-failed"
		case line == "Missing PCS cookies from the request":
			return "source-adp-approval-required"
		}
		// The REST wrapper quotes Apple's body. Require both its exact 423
		// envelope and a known reason, not merely a 423 or a substring in a URL.
		if quoted, ok := strings.CutPrefix(line, "HTTP error 423 (423 Locked) returned body: "); ok {
			body, err := strconv.Unquote(quoted)
			if err != nil {
				continue
			}
			if body == "Missing PCS cookies from the request" {
				return "source-adp-approval-required"
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal([]byte(body), &fields) != nil {
				continue
			}
			for _, key := range []string{"reason", "message"} {
				var reason string
				if json.Unmarshal(fields[key], &reason) == nil && reason == "Missing PCS cookies from the request" {
					return "source-adp-approval-required"
				}
			}
		}
	}
	return fallback
}

func (s *Source) environment() []string {
	var env []string
	for _, variable := range os.Environ() {
		key, _, _ := strings.Cut(variable, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "RCLONE") {
			continue
		}
		switch upper {
		case "HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "TMPDIR", "TMP", "TEMP":
			continue
		}
		// Operator proxy variables are deliberately retained.
		env = append(env, variable)
	}
	for key, directory := range map[string]string{
		"HOME": "home", "XDG_CONFIG_HOME": "xdg-config", "XDG_CACHE_HOME": "xdg-cache",
		"XDG_DATA_HOME": "home", "XDG_STATE_HOME": "home", "XDG_RUNTIME_DIR": "tmp",
		"TMPDIR": "tmp", "TMP": "tmp", "TEMP": "tmp",
	} {
		env = append(env, key+"="+filepath.Join(s.dir, directory))
	}
	return env
}

type privateStderr struct{ buffer bytes.Buffer }

func (b *privateStderr) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := (64 << 10) - b.buffer.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buffer.Write(p)
	}
	return n, nil
}

func (s *Source) run(ctx context.Context, timeout time.Duration, args []string, stdout io.Writer, limit int64, fallback string) error {
	if s.closed {
		return failure("source-unavailable", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	stopParent := context.AfterFunc(s.ctx, cancel)
	defer func() { stopParent(); cancel() }()
	if s.ctx.Err() != nil {
		return failure("source-unavailable", s.ctx.Err())
	}
	if ctx.Err() != nil {
		return failure("source-unavailable", ctx.Err())
	}
	// No flags are caller supplied. Credentials reside only in the private INI.
	flags := []string{"--config", filepath.Join(s.dir, configName), "--cache-dir", filepath.Join(s.dir, "cache"),
		"--log-level", "ERROR", "--stats", "0", "--retries", "2", "--low-level-retries", "2", "--retries-sleep", "1s"}
	cmd := exec.CommandContext(ctx, s.binary, append(flags, args...)...)
	cmd.Env = s.environment()
	cmd.Dir = s.dir
	cmd.Stdin = nil // exec maps nil to /dev/null; no prompts or inherited terminal.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	kill := func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	cmd.Cancel = func() error { kill(); return nil }
	cmd.WaitDelay = time.Second
	output := &boundedWriter{dst: stdout, remaining: limit, kill: kill}
	var stderr privateStderr
	cmd.Stdout, cmd.Stderr = output, &stderr
	err := cmd.Start()
	if err == nil {
		err = cmd.Wait()
		// WaitDelay also handles a child that exits while its descendants hold
		// pipes open. Kill its group even when the leader has already exited.
		kill()
	}
	code := classify(stderr.buffer.String(), fallback)
	var result error
	switch {
	case s.ctx.Err() != nil:
		result = failure(code, s.ctx.Err())
	case ctx.Err() != nil:
		result = failure(code, ctx.Err())
	case output.exceeded:
		if args[0] == "cat" {
			result = failure("source-file-mutated", nil)
		} else if args[0] == "lsjson" {
			result = failure("source-directory-limit", nil)
		} else {
			result = failure(fallback, nil)
		}
	case output.failed:
		result = failure(fallback, nil)
	case err != nil:
		result = failure(code, nil)
	}
	previous := s.config
	harvested := s.harvest()
	if result != nil && (code == "source-auth-failed" || code == "source-auth-expired") {
		// A failed auth path is not evidence of a usable refreshed session.
		s.config = previous
	}
	if restoreErr := s.writeConfig(); result == nil && restoreErr != nil {
		result = restoreErr
	}
	if result == nil && !harvested {
		result = failure("source-config-invalid", nil)
	}
	return result
}
