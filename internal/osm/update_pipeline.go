package osm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type CommandPipeline struct {
	DatabaseURL string
	Root        string
	Log         io.Writer

	outputCommand    func(context.Context, []string, string, ...string) (string, error)
	retryDelays      []time.Duration
	progressInterval time.Duration
}

var postgresRetryDelays = []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second, 60 * time.Second}

var pipelineSQLFiles = map[string]string{
	"postprocess": "postprocess.sql", "derive": "derive-batched.sql",
	"clip-candidates": "clip-localities-candidates.sql", "clip-replacements": "clip-localities-replacements.sql",
	"clip-apply": "clip-localities-apply.sql", "clip-residual": "clip-localities-residual.sql", "clip-finalize": "clip-localities-finalize.sql",
	"attribute-parks-tags": "attribute-parks-batched.sql", "attribute-education": "attribute-education-batched.sql",
	"attribute-slivers": "attribute-slivers-batched.sql", "identity-segments": "identity-segments-batched.sql",
	"identity-edges": "identity-edges-batched.sql", "identity-proximity": "identity-proximity-batched.sql",
	"identity-components": "identity-components.sql", "identity-propagate": "identity-propagate-batched.sql",
	"identity-label-overrides": "identity-label-overrides-batched.sql", "identity-rewrite": "identity-rewrite-batched.sql",
	"prepare-partitions": "prepare-partitions.sql", "validate": "validate.sql",
}

type commandFailure struct {
	name   string
	output string
	cause  error
}

func (e *commandFailure) Error() string {
	message := safeFailure(errors.New(e.output))
	if message == "" {
		message = safeFailure(e.cause)
	}
	return fmt.Sprintf("%s failed: %s", e.name, message)
}

func (e *commandFailure) Unwrap() error { return e.cause }

func (p CommandPipeline) Versions(ctx context.Context) (ToolVersions, error) {
	quiet := p
	quiet.Log = nil
	osmium, err := quiet.output(ctx, nil, "osmium", "--version")
	if err != nil {
		return ToolVersions{}, err
	}
	osm2pgsql, err := quiet.output(ctx, nil, "osm2pgsql", "--version")
	if err != nil {
		return ToolVersions{}, err
	}
	return ToolVersions{Osmium: reportedVersion(osmium, "osmium"), Osm2pgsql: reportedVersion(osm2pgsql, "osm2pgsql")}, nil
}

func (p CommandPipeline) StageFence(stage string) (string, error) {
	value := []byte("pipeline-v1:" + stage)
	if name, ok := pipelineSQLFiles[stage]; ok {
		contents, err := os.ReadFile(filepath.Join(p.Root, name))
		if err != nil {
			return "", err
		}
		value = append(contents, []byte("\npipeline-v2:"+stage)...)
	}
	sum := sha256.Sum256(value)
	return fmt.Sprintf("%x", sum), nil
}

func (p CommandPipeline) Maintain(ctx context.Context, stage string, generation Generation) error {
	if stage != "identity-propagate" {
		return nil
	}
	if generation.ID < 1 || generation.SchemaName != fmt.Sprintf("osm_build_%d", generation.ID) {
		return fmt.Errorf("invalid build schema %q", generation.SchemaName)
	}
	environment, _, err := postgresConnectionEnvironment(p.DatabaseURL)
	if err != nil {
		return err
	}
	statement := fmt.Sprintf("VACUUM %q.attribution_identity_components", generation.SchemaName)
	_, err = p.outputWithProgress(ctx, environment, "OSM maintenance identity-propagate vacuum", "OSM maintenance identity-propagate vacuum", "psql",
		"-X", "--no-psqlrc", "--set=ON_ERROR_STOP=1", "--command", statement)
	return err
}

func reportedVersion(output, tool string) string {
	prefix := tool + " version "
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, prefix))
		if len(fields) == 1 || (len(fields) == 2 && (fields[1] == "("+fields[0]+")" || fields[1] == "(v"+fields[0]+")")) {
			return fields[0]
		}
		return "unreported"
	}
	return "unreported"
}

func (p CommandPipeline) SourceTimestamp(ctx context.Context, source string) (time.Time, error) {
	output, err := p.output(ctx, nil, "osmium", "fileinfo", "--json", source)
	if err != nil {
		return time.Time{}, err
	}
	return parseSourceTimestamp([]byte(output))
}

func parseSourceTimestamp(output []byte) (time.Time, error) {
	var info struct {
		Header struct {
			Option  map[string]string `json:"option"`
			Options map[string]string `json:"options"`
		} `json:"header"`
	}
	if err := json.Unmarshal(output, &info); err != nil {
		return time.Time{}, fmt.Errorf("decode osmium fileinfo: %w", err)
	}
	options := info.Header.Option
	if len(options) == 0 {
		options = info.Header.Options
	}
	value := options["osmosis_replication_timestamp"]
	if value == "" {
		value = options["timestamp"]
	}
	timestamp, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid osmium source timestamp %q", value)
	}
	return timestamp, nil
}

func (p CommandPipeline) Run(ctx context.Context, stage string, generation Generation, source, filtered string) ([]byte, error) {
	environment, databaseName, err := postgresConnectionEnvironment(p.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("configure OSM database connection: %w", err)
	}
	environment = append(environment, "OSM_BUILD_SCHEMA="+generation.SchemaName)
	variables := []string{
		"--set=ON_ERROR_STOP=1", "--set=OSM_BUILD_SCHEMA=" + generation.SchemaName,
		fmt.Sprintf("--set=OSM_GENERATION_ID=%d", generation.ID), "--set=OSM_REGION_ID=" + generation.RegionID,
		fmt.Sprintf("--set=OSM_IMPORTER_VERSION=%d", ImporterVersion), fmt.Sprintf("--set=OSM_DERIVATION_VERSION=%d", DerivationVersion),
	}
	switch stage {
	case "tags-filter":
		_, err := p.output(ctx, nil, "osmium", "tags-filter", "--overwrite", "--output", filtered, source,
			"w/highway", "w/leisure=park,nature_reserve", "w/boundary=protected_area,national_park",
			"w/protected_area=national_park", "w/protection_title", "w/amenity=school,college,university", "w/landuse=education",
			"r/boundary=administrative,protected_area,national_park", "r/leisure=park,nature_reserve",
			"r/protected_area=national_park", "r/protection_title", "r/amenity=school,college,university", "r/landuse=education")
		return nil, err
	case "check-refs":
		// The filter retains available relation members so municipality and park
		// multipolygons can be assembled alongside complete highway node lineage.
		_, err := p.output(ctx, nil, "osmium", "check-refs", filtered)
		return nil, err
	case "osm2pgsql":
		_, err := p.output(ctx, environment, "osm2pgsql", "--database", databaseName, "--create", "--slim", "--drop", "--output=flex", "--style", filepath.Join(p.Root, "import.lua"), "--schema", generation.SchemaName, "--middle-schema", generation.SchemaName, filtered)
		return nil, err
	case "postprocess", "derive", "clip-candidates", "clip-replacements", "clip-apply", "clip-residual", "clip-finalize", "attribute-parks-tags", "attribute-education", "attribute-slivers", "identity-segments", "identity-edges", "identity-proximity", "identity-components", "identity-propagate", "identity-label-overrides", "identity-rewrite", "prepare-partitions", "validate":
		args := []string{"-X", "--no-psqlrc"}
		if stage != "validate" && !batchStages[stage] {
			args = append(args, "--single-transaction")
		}
		args = append(args, variables...)
		if stage == "validate" {
			args = append(args, "--tuples-only", "--no-align")
		}
		args = append(args, "--file", filepath.Join(p.Root, pipelineSQLFiles[stage]))
		if atomicSQLStages[stage] {
			checkpoint := fmt.Sprintf(`SELECT osm_catalog.complete_generation_stage(%d,'%s',checkpoint.batch_count,
				checkpoint.cursor||'{"done":true}'::jsonb,checkpoint.rows_processed)
				FROM osm_catalog.generation_stages checkpoint WHERE checkpoint.generation_id=%d AND checkpoint.stage='%s'`,
				generation.ID, stage, generation.ID, stage)
			args = append(args, "--command", checkpoint)
		}
		return p.runPostgresStage(ctx, stage, generation.BatchSize, environment, args)
	default:
		return nil, fmt.Errorf("unknown OSM stage %q", stage)
	}
}

func (p CommandPipeline) runPostgresStage(ctx context.Context, stage string, batchSize int, environment, args []string) ([]byte, error) {
	delays := p.retryDelays
	if delays == nil {
		delays = postgresRetryDelays
	}
	if sqlCheckpointStage(stage) {
		delays = nil
	}
	maximumAttempts := len(delays) + 1
	for attempt := 1; ; attempt++ {
		progressLabel := fmt.Sprintf("OSM batch %s", stage)
		label := progressLabel
		if batchSize > 0 {
			label += fmt.Sprintf(" (batch size: %d)", batchSize)
		}
		if maximumAttempts > 1 {
			label = fmt.Sprintf("%s attempt %d/%d", label, attempt, maximumAttempts)
			progressLabel = fmt.Sprintf("%s attempt %d/%d", progressLabel, attempt, maximumAttempts)
		}
		output, err := p.outputWithProgress(ctx, environment, label, progressLabel, "psql", args...)
		if err == nil {
			return []byte(output), nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !isTransientPostgresFailure(err) {
			return nil, err
		}
		if attempt == maximumAttempts {
			p.logRetry(stage, attempt, maximumAttempts, 0, err, true)
			return nil, err
		}

		delay := delays[attempt-1]
		p.logRetry(stage, attempt, maximumAttempts, delay, err, false)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (p CommandPipeline) logRetry(stage string, attempt, maximumAttempts int, delay time.Duration, err error, exhausted bool) {
	if p.Log == nil {
		return
	}
	if exhausted {
		_, _ = fmt.Fprintf(p.Log, "OSM stage %s transient PostgreSQL failure on attempt %d/%d; retries exhausted: %s\n",
			stage, attempt, maximumAttempts, safeFailure(err))
		return
	}
	_, _ = fmt.Fprintf(p.Log, "OSM stage %s transient PostgreSQL failure on attempt %d/%d; retrying in %s: %s\n",
		stage, attempt, maximumAttempts, delay, safeFailure(err))
}

func isTransientPostgresFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	message := err.Error()
	var failure *commandFailure
	if errors.As(err, &failure) {
		message = failure.output + " " + failure.cause.Error()
	}
	message = strings.ToLower(message)
	for _, marker := range []string{
		"unexpected eof",
		"connection to server was lost",
		"connection to server was closed",
		"server closed connection",
		"server closed the connection",
		"connection reset",
		"broken pipe",
		"timeout connecting",
		"tls timeout",
		"tls handshake timeout",
		"i/o timeout",
		"could not connect",
		"connection refused",
		"recovery conflict",
		"conflict with recovery",
		"sqlstate 40001",
		"administrator termination",
		"administrator command",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func postgresConnectionEnvironment(raw string) ([]string, string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() == "" {
		return nil, "", fmt.Errorf("invalid PostgreSQL URL")
	}
	databaseName := strings.TrimPrefix(parsed.EscapedPath(), "/")
	databaseName, err = url.PathUnescape(databaseName)
	if err != nil || databaseName == "" || strings.Contains(databaseName, "/") {
		return nil, "", fmt.Errorf("invalid PostgreSQL database name")
	}
	environment := []string{"PGHOST=" + parsed.Hostname(), "PGDATABASE=" + databaseName}
	if port := parsed.Port(); port != "" {
		environment = append(environment, "PGPORT="+port)
	}
	if parsed.User != nil {
		environment = append(environment, "PGUSER="+parsed.User.Username())
		if password, present := parsed.User.Password(); present {
			environment = append(environment, "PGPASSWORD="+password)
		}
	}
	for queryName, environmentName := range map[string]string{
		"sslmode": "PGSSLMODE", "sslcert": "PGSSLCERT", "sslkey": "PGSSLKEY", "sslrootcert": "PGSSLROOTCERT",
	} {
		if value := parsed.Query().Get(queryName); value != "" {
			environment = append(environment, environmentName+"="+value)
		}
	}
	return environment, databaseName, nil
}

func (p CommandPipeline) output(ctx context.Context, environment []string, name string, args ...string) (string, error) {
	label := "OSM tool " + name
	return p.outputWithProgress(ctx, environment, label, label, name, args...)
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

type carriageReturnLineWriter struct {
	mu          sync.Mutex
	w           io.Writer
	pendingCRLF bool
}

func (w *carriageReturnLineWriter) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	normalized := make([]byte, 0, len(value))
	for _, character := range value {
		if w.pendingCRLF {
			w.pendingCRLF = false
			if character == '\n' {
				continue
			}
		}
		if character == '\r' {
			normalized = append(normalized, '\n')
			w.pendingCRLF = true
			continue
		}
		normalized = append(normalized, character)
	}
	if len(normalized) > 0 {
		if _, err := w.w.Write(normalized); err != nil {
			return 0, err
		}
	}
	return len(value), nil
}

func (w *lockedWriter) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(value)
}

func (p CommandPipeline) outputWithProgress(ctx context.Context, environment []string, label, progressLabel, name string, args ...string) (string, error) {
	interval := p.progressInterval
	if interval == 0 {
		interval = time.Minute
	}
	var log io.Writer
	if p.Log != nil {
		log = &lockedWriter{w: p.Log}
		_, _ = fmt.Fprintf(log, "Running %s\n", label)
	}
	started := time.Now()
	done := make(chan struct{})
	var heartbeatDone chan struct{}
	if log != nil && interval > 0 {
		heartbeatDone = make(chan struct{})
		go func() {
			defer close(heartbeatDone)
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					_, _ = fmt.Fprintf(log, "%s still running (elapsed %s)\n", progressLabel, time.Since(started).Round(time.Second))
				}
			}
		}()
	}
	defer func() {
		close(done)
		if heartbeatDone != nil {
			<-heartbeatDone
		}
	}()

	if p.outputCommand != nil {
		return p.outputCommand(ctx, environment, name, args...)
	}
	command := exec.CommandContext(ctx, name, args...)
	command.Env = append(os.Environ(), environment...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if log != nil {
		commandLog := log
		if name == "osm2pgsql" {
			commandLog = &carriageReturnLineWriter{w: log}
		}
		command.Stdout = io.MultiWriter(&stdout, commandLog)
		command.Stderr = io.MultiWriter(&stderr, commandLog)
	}
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		output := stderr.String()
		if strings.TrimSpace(output) == "" {
			output = stdout.String()
		}
		return "", &commandFailure{name: name, output: output, cause: err}
	}
	output := stdout.String()
	if strings.TrimSpace(output) == "" {
		output = stderr.String()
	}
	return output, nil
}
