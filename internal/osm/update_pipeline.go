package osm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type CommandPipeline struct {
	DatabaseURL string
	Root        string
	Log         io.Writer
}

func (p CommandPipeline) Versions(ctx context.Context) (ToolVersions, error) {
	osmium, err := p.output(ctx, nil, "osmium", "--version")
	if err != nil {
		return ToolVersions{}, err
	}
	osm2pgsql, err := p.output(ctx, nil, "osm2pgsql", "--version")
	if err != nil {
		return ToolVersions{}, err
	}
	return ToolVersions{Osmium: reportedVersion(osmium, "osmium"), Osm2pgsql: reportedVersion(osm2pgsql, "osm2pgsql")}, nil
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
		_, err := p.output(ctx, nil, "osmium", "tags-filter", "--overwrite", "--output", filtered, source, "w/highway", "r/boundary=administrative")
		return nil, err
	case "check-refs":
		// Geofabrik boundary relations may reference members outside the clipped
		// extract; only complete highway way-node lineage is a promotion gate.
		_, err := p.output(ctx, nil, "osmium", "check-refs", filtered)
		return nil, err
	case "osm2pgsql":
		_, err := p.output(ctx, environment, "osm2pgsql", "--database", databaseName, "--create", "--slim", "--drop", "--output=flex", "--style", filepath.Join(p.Root, "import.lua"), "--schema", generation.SchemaName, "--middle-schema", generation.SchemaName, filtered)
		return nil, err
	case "postprocess", "derive", "clip", "prepare-partitions", "validate":
		files := map[string]string{"postprocess": "postprocess.sql", "derive": "derive-compact.sql", "clip": "clip-localities.sql", "prepare-partitions": "prepare-partitions.sql", "validate": "validate.sql"}
		args := []string{"-X", "--no-psqlrc"}
		args = append(args, variables...)
		if stage == "validate" {
			args = append(args, "--tuples-only", "--no-align")
		}
		args = append(args, "--file", filepath.Join(p.Root, files[stage]))
		output, err := p.output(ctx, environment, "psql", args...)
		return []byte(output), err
	default:
		return nil, fmt.Errorf("unknown OSM stage %q", stage)
	}
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
	command := exec.CommandContext(ctx, name, args...)
	command.Env = append(os.Environ(), environment...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if p.Log != nil {
		_, _ = fmt.Fprintf(p.Log, "running OSM tool %s\n", name)
	}
	if err := command.Run(); err != nil {
		message := safeFailure(errors.New(stderr.String()))
		if message == "" {
			message = safeFailure(errors.New(stdout.String()))
		}
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("%s failed: %s", name, message)
	}
	output := stdout.String()
	if strings.TrimSpace(output) == "" {
		output = stderr.String()
	}
	return output, nil
}
