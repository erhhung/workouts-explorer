package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This suite never uses ambient role URLs without the explicit synthetic-test
// switch and a matching named loopback test database. It creates no databases.
func tileTestDatabases(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	if os.Getenv("MAP_TILE_TEST_DISPOSABLE") != "1" {
		t.Skip("MAP_TILE_TEST_DISPOSABLE=1 and named loopback test role URLs required")
	}
	var endpoint string
	var pools []*pgxpool.Pool
	for _, variable := range []string{"API_DATABASE_URL", "MIGRATION_DATABASE_URL"} {
		raw := os.Getenv(variable)
		cfg, err := pgx.ParseConfig(raw)
		if raw == "" || err != nil {
			t.Fatal("explicit disposable role URL required: " + variable)
		}
		name := strings.ToLower(cfg.Database)
		if (cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1") ||
			(!strings.HasPrefix(name, "test_") && !strings.Contains(name, "_test")) ||
			strings.Contains(name, "clone") || strings.Contains(name, "actual") || strings.Contains(name, "live") {
			t.Fatal("tile fixtures require a named loopback test database, never a live/actual-data clone")
		}
		for _, fallback := range cfg.Fallbacks {
			if fallback.Host != cfg.Host || fallback.Port != cfg.Port {
				t.Fatal("alternate database endpoints prohibited")
			}
		}
		target := cfg.Host + ":" + strconv.Itoa(int(cfg.Port)) + "/" + cfg.Database
		if endpoint != "" && endpoint != target {
			t.Fatal("tile test roles must address the same disposable database")
		}
		endpoint = target
		pool, err := pgxpool.New(context.Background(), raw)
		if err != nil {
			t.Fatal("cannot open tile test database")
		}
		t.Cleanup(pool.Close)
		pools = append(pools, pool)
	}
	for index, role := range []string{"workouts_api", "workouts_migration"} {
		var actual string
		if err := pools[index].QueryRow(context.Background(), `SELECT current_user`).Scan(&actual); err != nil || actual != role {
			t.Fatal("tile integration fixtures require the actual runtime/migration roles")
		}
	}
	return pools[0], pools[1]
}

func TestTileAuthorizationCacheAndFetchIntegration(t *testing.T) {
	apiDB, adminDB := tileTestDatabases(t)
	ctx := context.Background()
	for _, coverage := range []bool{false, true} {
		for _, duringFetch := range []bool{false, true} {
			for _, mutation := range []string{"delete", "revoke", "expire_session", "disable", "generation", "context", "context_revision"} {
				if strings.HasPrefix(mutation, "context") && !coverage {
					continue
				}
				t.Run(mapTileKey{coverage: coverage}.kind()+"/"+mutation+"/during_fetch="+strconv.FormatBool(duringFetch), func(t *testing.T) {
					principal, account := insertSourceTestUser(t, adminDB)
					bearer := insertTestSession(t, apiDB, principal, "bearer", "")
					server := integrationServer(t, apiDB, &recordingSender{})
					created := httptest.NewRecorder()
					request := httptest.NewRequest(http.MethodPost, "/api/map-selections", strings.NewReader(`{"startDate":"2026-03-07","endDate":"2026-03-09"}`))
					request.Header.Set("Authorization", "Bearer "+bearer)
					server.CreateMapSelection(created, request, generated.CreateMapSelectionParams{})
					var selection generated.MapSelection
					if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &selection) != nil {
						t.Fatalf("selection status=%d", created.Code)
					}
					selectionID, _ := parseCompactUUID(selection.Id)
					var session uuid.UUID
					if err := adminDB.QueryRow(ctx, `SELECT session_id FROM app.map_selections WHERE account_id=$1 AND id=$2`, account, selectionID).Scan(&session); err != nil {
						t.Fatal("cannot read fixture session")
					}
					invalidate := func() error {
						var err error
						switch mutation {
						case "delete":
							_, err = adminDB.Exec(ctx, `DELETE FROM app.map_selections WHERE account_id=$1 AND id=$2`, account, selectionID)
						case "revoke":
							_, err = adminDB.Exec(ctx, `UPDATE app.sessions SET revoked_at=clock_timestamp() WHERE id=$1`, session)
						case "expire_session":
							_, err = adminDB.Exec(ctx, `UPDATE app.sessions SET expires_at=created_at+interval '1 microsecond' WHERE id=$1`, session)
						case "disable":
							_, err = adminDB.Exec(ctx, `UPDATE app.authentication_principals SET disabled_at=clock_timestamp() WHERE id=$1`, principal)
						case "generation":
							_, err = adminDB.Exec(ctx, `UPDATE app.account_data_generations SET generation=generation+1 WHERE account_id=$1`, account)
						case "context":
							_, err = adminDB.Exec(ctx, `DELETE FROM app.coverage_display_contexts WHERE account_id=$1`, account)
						case "context_revision":
							_, err = adminDB.Exec(ctx, `UPDATE app.coverage_display_revisions SET revision=revision+1 WHERE account_id=$1`, account)
						}
						return err
					}
					var calls atomic.Int64
					server.config.TileServerURL = "http://tiles.invalid"
					server.tileClient = &http.Client{Transport: tileRoundTripper(func(r *http.Request) (*http.Response, error) {
						calls.Add(1)
						wantPath := "/app.raw_route_mvt/0/0/0"
						if coverage {
							wantPath = "/app.coverage_mvt/0/0/0"
						}
						if r.URL.Path != wantPath {
							t.Errorf("upstream path=%q, want %q", r.URL.Path, wantPath)
						}
						query := r.URL.Query()
						if len(query) != 4 || query.Get("target_account_id") != account.String() ||
							query.Get("target_session_id") != session.String() || query.Get("target_selection_id") != selectionID.String() ||
							query.Get("target_generation") != strconv.FormatInt(selection.DataGeneration, 10) {
							t.Error("upstream did not receive exactly the authorized scope")
						}
						if duringFetch {
							if err := invalidate(); err != nil {
								return nil, err
							}
						}
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-protobuf"}},
							Body: io.NopCloser(strings.NewReader("PRIVATE-TILE-BYTES"))}, nil
					})}
					t.Cleanup(server.mapTiles().close)
					get := func() *httptest.ResponseRecorder {
						w := httptest.NewRecorder()
						r := httptest.NewRequest(http.MethodGet, "/api/map-selections/"+selection.Id, nil)
						r.Header.Set("Authorization", "Bearer "+bearer)
						if coverage {
							server.GetMapSelectionCoverageTile(w, r, generated.MapSelectionID(selection.Id), selection.DataGeneration, 0, 0, 0)
						} else {
							server.GetMapSelectionRouteTile(w, r, generated.MapSelectionID(selection.Id), selection.DataGeneration, 0, 0, 0)
						}
						return w
					}
					if !duringFetch {
						if response := get(); response.Code != 200 || response.Body.String() != "PRIVATE-TILE-BYTES" {
							t.Fatalf("warm status=%d", response.Code)
						}
						if get().Code != 200 || calls.Load() != 1 {
							t.Fatal("authorized warm tile did not reuse bytes")
						}
						if err := invalidate(); err != nil {
							t.Fatal("cannot invalidate fixture")
						}
					}
					response := get()
					want := 404
					if !duringFetch && (mutation == "revoke" || mutation == "disable" || mutation == "expire_session") {
						want = 401
					}
					if response.Code != want || strings.Contains(response.Body.String(), "PRIVATE-TILE-BYTES") ||
						response.Header().Get("Cache-Control") != "private, no-store" || calls.Load() != 1 {
						t.Fatalf("invalid capability response=%d want=%d upstream_calls=%d", response.Code, want, calls.Load())
					}
					tileTestState(t, server.mapTiles(), func(s *mapTileService) bool { return s.occupied == 0 })
				})
			}
		}
	}
}

type tileSlowDeliveryWriter struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	resume  chan struct{}
}

func (w *tileSlowDeliveryWriter) Write(body []byte) (int, error) {
	close(w.entered)
	<-w.resume
	return len(body), nil
}

func TestTileSlowDeliveryBackpressureIntegration(t *testing.T) {
	apiDB, adminDB := tileTestDatabases(t)
	principal, _ := insertSourceTestUser(t, adminDB)
	bearer := insertTestSession(t, apiDB, principal, "bearer", "")
	server := integrationServer(t, apiDB, &recordingSender{})
	request := func(method, path, body string) *http.Request {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+bearer)
		return r
	}
	created := httptest.NewRecorder()
	server.CreateMapSelection(created, request(http.MethodPost, "/api/map-selections",
		`{"startDate":"2026-03-07","endDate":"2026-03-09"}`), generated.CreateMapSelectionParams{})
	var selection generated.MapSelection
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &selection) != nil {
		t.Fatal("cannot capture slow-delivery selection")
	}
	server.config.MapTiles = server.config.MapTiles.WithDefaults()
	server.config.MapTiles.Concurrency, server.config.MapTiles.CacheBytes = 1, 0
	server.config.TileServerURL = "http://tiles.invalid"
	var calls atomic.Int64
	server.tileClient = &http.Client{Transport: tileRoundTripper(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-protobuf"}},
			Body: io.NopCloser(strings.NewReader(strings.Repeat("x", maximumTileBytes)))}, nil
	})}
	t.Cleanup(server.mapTiles().close)
	w := &tileSlowDeliveryWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), resume: make(chan struct{})}
	resume := sync.OnceFunc(func() { close(w.resume) })
	t.Cleanup(resume)
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		server.GetMapSelectionRouteTile(w, request(http.MethodGet, "/", ""), generated.MapSelectionID(selection.Id), selection.DataGeneration, 1, 0, 0)
	}()
	tileTestReceive(t, w.entered)
	second := httptest.NewRecorder()
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		server.GetMapSelectionRouteTile(second, request(http.MethodGet, "/", ""), generated.MapSelectionID(selection.Id), selection.DataGeneration, 1, 1, 0)
	}()
	tileTestState(t, server.mapTiles(), func(s *mapTileService) bool { return s.active == 0 && s.occupied == 1 && s.queue.Len() == 1 })
	if calls.Load() != 1 {
		t.Fatal("blocked downstream write allowed another full response allocation")
	}
	resume()
	tileTestReceive(t, firstDone)
	tileTestReceive(t, secondDone)
	if w.Code != 200 || second.Code != 200 || calls.Load() != 2 {
		t.Fatalf("delivery result first=%d second=%d calls=%d", w.Code, second.Code, calls.Load())
	}
	tileTestState(t, server.mapTiles(), func(s *mapTileService) bool { return s.occupied == 0 })
}

func TestTileSelectionExpiryIntegration(t *testing.T) {
	apiDB, adminDB := tileTestDatabases(t)
	principal, account := insertSourceTestUser(t, adminDB)
	bearer := insertTestSession(t, apiDB, principal, "bearer", "")
	server := integrationServer(t, apiDB, &recordingSender{})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+bearer)
	session, err := server.authenticate(r.Context(), r)
	if err != nil {
		t.Fatal("cannot authenticate fixture")
	}
	tx, err := server.sessionAccountTransactionWithOptions(r.Context(), session, pgx.TxOptions{})
	if err != nil {
		t.Fatal("cannot open expiry fixture")
	}
	defer tx.Rollback(r.Context())
	id := uuid.New()
	var generation int64
	var expires time.Time
	err = tx.QueryRow(r.Context(), `INSERT INTO app.map_selections(id,account_id,session_id,generation,expires_at,start_date,end_date,selection_kind)
		SELECT $1,$2,$3,generation,clock_timestamp()+interval '200 milliseconds','2026-03-07','2026-03-09','explicit_subset'
		FROM app.account_data_generations WHERE account_id=$2 RETURNING generation,expires_at`, id, account, session.sessionID).Scan(&generation, &expires)
	if err != nil || tx.Commit(r.Context()) != nil {
		t.Fatal("cannot capture expiring selection")
	}
	server.config.TileServerURL = "http://tiles.invalid"
	server.tileClient = &http.Client{Transport: tileRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 204, Header: http.Header{}, Body: http.NoBody}, nil
	})}
	t.Cleanup(server.mapTiles().close)
	get := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		server.GetMapSelectionRouteTile(w, r, generated.MapSelectionID(compactUUID(id)), generation, 0, 0, 0)
		return w
	}
	if get().Code != 200 {
		t.Fatal("unexpired selection unavailable")
	}
	time.Sleep(time.Until(expires) + 10*time.Millisecond)
	if response := get(); response.Code != 404 {
		t.Fatalf("expired tile status=%d", response.Code)
	}
}

type tileCachedDeliveryWriter struct {
	*httptest.ResponseRecorder
	service *mapTileService
	key     mapTileKey
	entered chan struct{}
	once    sync.Once
}

func (w *tileCachedDeliveryWriter) Header() http.Header {
	w.service.mu.Lock()
	entry := w.service.cache[w.key]
	pinned := entry != nil && entry.Value.(*mapTileEntry).readers > 0
	w.service.mu.Unlock()
	if pinned {
		w.once.Do(func() { close(w.entered) })
	}
	return w.ResponseRecorder.Header()
}

func TestTileCachedDeliveryAuthorizationAfterEncoderAdmissionIntegration(t *testing.T) {
	apiDB, adminDB := tileTestDatabases(t)
	for _, coverage := range []bool{false, true} {
		for _, mutation := range []string{"delete", "revoke", "expire_session", "cancel"} {
			t.Run(mapTileKey{coverage: coverage}.kind()+"/"+mutation, func(t *testing.T) {
				principal, account := insertSourceTestUser(t, adminDB)
				bearer := insertTestSession(t, apiDB, principal, "bearer", "")
				server := integrationServer(t, apiDB, &recordingSender{})
				request := httptest.NewRequest(http.MethodPost, "/api/map-selections", strings.NewReader(`{"startDate":"2026-03-07","endDate":"2026-03-09"}`))
				request.Header.Set("Authorization", "Bearer "+bearer)
				created := httptest.NewRecorder()
				server.CreateMapSelection(created, request, generated.CreateMapSelectionParams{})
				var selection generated.MapSelection
				if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &selection) != nil {
					t.Fatal("cannot create blocked-encoder selection")
				}
				selectionID, _ := parseCompactUUID(selection.Id)
				key := mapTileKey{account: account, selection: selectionID, generation: selection.DataGeneration, coverage: coverage}
				if err := adminDB.QueryRow(context.Background(), `SELECT session_id FROM app.map_selections WHERE account_id=$1 AND id=$2`, account, selectionID).Scan(&key.session); err != nil {
					t.Fatal("cannot read blocked-encoder session")
				}
				body := strings.Repeat("PRIVATE-TILE-BYTES", 100)
				var calls atomic.Int64
				server.config.TileServerURL = "http://tiles.invalid"
				server.tileClient = &http.Client{Transport: tileRoundTripper(func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-protobuf"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
				})}
				service := server.mapTiles()
				t.Cleanup(service.close)
				get := func(w http.ResponseWriter, r *http.Request) {
					if coverage {
						server.GetMapSelectionCoverageTile(w, r, generated.MapSelectionID(selection.Id), selection.DataGeneration, 0, 0, 0)
					} else {
						server.GetMapSelectionRouteTile(w, r, generated.MapSelectionID(selection.Id), selection.DataGeneration, 0, 0, 0)
					}
				}
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.Header.Set("Authorization", "Bearer "+bearer)
				warm := httptest.NewRecorder()
				get(warm, r)
				if warm.Code != 200 || warm.Body.String() != body {
					t.Fatal("cannot warm blocked-encoder cache")
				}

				busy := httptest.NewRequest(http.MethodGet, "/", nil)
				busy.Header.Set("Accept-Encoding", "gzip")
				resume, completed := make(chan struct{}), make(chan struct{}, maximumTileCompressors)
				release := sync.OnceFunc(func() {
					close(resume)
					for range maximumTileCompressors {
						tileTestReceive(t, completed)
					}
				})
				t.Cleanup(release)
				for range maximumTileCompressors {
					w := &tileCompressionWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), resume: resume}
					go func() {
						writeMapTileResult(w, busy, "route", mapTileResult{status: 200, body: []byte(body), outcome: "success"}, allowTileTestDelivery)
						completed <- struct{}{}
					}()
					tileTestReceive(t, w.entered)
				}
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				r = r.WithContext(ctx)
				r.Header.Set("Accept-Encoding", "gzip")
				w := &tileCachedDeliveryWriter{ResponseRecorder: httptest.NewRecorder(), service: service, key: key, entered: make(chan struct{})}
				done := make(chan struct{})
				go func() { get(w, r); close(done) }()
				// Header access with a pinned cache body places this barrier inside
				// delivery, after any incorrectly early handler authorization.
				tileTestReceive(t, w.entered)
				if len(tileCompressors) != 0 || apiDB.Stat().AcquiredConns() != 0 {
					t.Fatal("encoder wait did not stay bounded or retained a database connection")
				}
				var err error
				switch mutation {
				case "delete":
					_, err = adminDB.Exec(context.Background(), `DELETE FROM app.map_selections WHERE account_id=$1 AND id=$2`, account, selectionID)
				case "revoke":
					_, err = adminDB.Exec(context.Background(), `UPDATE app.sessions SET revoked_at=clock_timestamp() WHERE id=$1`, key.session)
				case "expire_session":
					_, err = adminDB.Exec(context.Background(), `UPDATE app.sessions SET expires_at=created_at+interval '1 microsecond' WHERE id=$1`, key.session)
				case "cancel":
					cancel()
					tileTestReceive(t, done)
					if len(tileCompressors) != 0 {
						t.Fatal("canceled waiter consumed or released a busy encoder")
					}
				}
				if err != nil {
					t.Fatal("cannot invalidate blocked-encoder fixture")
				}
				release()
				if mutation != "cancel" {
					tileTestReceive(t, done)
				}
				want := 404
				if mutation == "cancel" {
					want = 503
				}
				if w.Code != want || strings.Contains(w.Body.String(), "PRIVATE-TILE-BYTES") || w.Header().Get("Content-Encoding") != "" ||
					w.Header().Get("Content-Type") != "application/problem+json" || calls.Load() != 1 || len(tileCompressors) != maximumTileCompressors {
					t.Fatalf("invalid blocked delivery: status=%d want=%d calls=%d encoders=%d", w.Code, want, calls.Load(), len(tileCompressors))
				}
				tileTestState(t, service, func(s *mapTileService) bool {
					entry := s.cache[key]
					return s.occupied == 0 && len(s.requests) == 0 && (entry == nil || entry.Value.(*mapTileEntry).readers == 0)
				})
			})
		}
	}
}
