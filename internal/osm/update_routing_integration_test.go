package osm

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestLibpqWriterRoutingPostgreSQL(t *testing.T) {
	raw := os.Getenv("OSM_ROUTING_TEST_URL")
	if raw == "" {
		t.Skip("explicit routing verification URL required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal("invalid verification URL")
	}
	hosts := strings.Split(parsed.Host, ",")
	if len(hosts) < 2 {
		t.Fatal("multiple primary/standby endpoints required")
	}
	// Put the initially configured primary last, forcing libpq to reject standbys.
	hosts = append(hosts[1:], hosts[0])
	parsed.Host = strings.Join(hosts, ",")
	environment, database, err := postgresConnectionEnvironment(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "psql", "-Xw", "--set=ON_ERROR_STOP=1", "--tuples-only", "--no-align", "--dbname", database, "--command", `SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'; BEGIN; CREATE TEMP TABLE osm_writer_routing_probe(id integer); DROP TABLE osm_writer_routing_probe; ROLLBACK;`)
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatal("primary-only libpq write probe failed")
	}
	if !strings.HasPrefix(strings.TrimSpace(string(output)), "t") {
		t.Fatal("write probe did not confirm the primary")
	}
}

func TestPostgresConnectionEnvironmentPreservesMultiHostWriterRouting(t *testing.T) {
	environment, database, err := postgresConnectionEnvironment("postgresql://writer:synthetic@replica-a:5433,primary:5434,replica-b:5435/osm?target_session_attrs=read-write&connect_timeout=5&sslmode=verify-full")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	if database != "osm" || values["PGHOST"] != "replica-a,primary,replica-b" || values["PGPORT"] != "5433,5434,5435" || values["PGTARGETSESSIONATTRS"] != "read-write" || values["PGCONNECT_TIMEOUT"] != "5" {
		t.Fatalf("writer routing was lost: hosts=%q ports=%q target=%q", values["PGHOST"], values["PGPORT"], values["PGTARGETSESSIONATTRS"])
	}
}
