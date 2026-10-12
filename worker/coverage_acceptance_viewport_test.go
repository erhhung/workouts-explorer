package worker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protowire"
)

const viewportBase = "http://127.0.0.1:18081"

type viewportTile struct {
	Z int `json:"z"`
	X int `json:"x"`
	Y int `json:"y"`
}

// CSS dimensions, not device pixels. Fractional zoom scales the canonical tile
// span; this planner deliberately supports only flat, unrotated Mercator.
func viewportPlan(width, height, lng, lat, zoom float64) ([]viewportTile, [2]float64, error) {
	for _, v := range []float64{width, height, lng, lat, zoom} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, [2]float64{}, errors.New("nonfinite viewport")
		}
	}
	if width <= 0 || height <= 0 || width > 8192 || height > 8192 || lng < -180 || lng >= 180 || math.Abs(lat) > 85.0511287798066 || zoom < 0 || zoom > 22 {
		return nil, [2]float64{}, errors.New("unsupported viewport")
	}
	z := int(math.Floor(zoom))
	n := math.Exp2(float64(z))
	center := [2]float64{(lng + 180) / 360 * n, (1 - math.Asinh(math.Tan(lat*math.Pi/180))/math.Pi) / 2 * n}
	scale := 512 * math.Exp2(zoom-float64(z))
	minX, maxX := int(math.Floor(center[0]-width/scale/2)), int(math.Ceil(center[0]+width/scale/2))-1
	minY, maxY := int(math.Floor(center[1]-height/scale/2)), int(math.Ceil(center[1]+height/scale/2))-1
	if minX < 0 || minY < 0 || maxX >= int(n) || maxY >= int(n) {
		return nil, center, errors.New("wrapped or polar viewport unsupported")
	}
	tiles := []viewportTile{}
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			tiles = append(tiles, viewportTile{z, x, y})
		}
	}
	// MapLibre geo/projection/covering_tiles.ts: centerPoint - 0.5 - x/y.
	distance := func(t viewportTile) float64 {
		x, y := center[0]-.5-float64(t.X), center[1]-.5-float64(t.Y)
		return x*x + y*y
	}
	sort.SliceStable(tiles, func(i, j int) bool { return distance(tiles[i]) < distance(tiles[j]) })
	return tiles, center, nil
}

func viewportBatchCount(value string) (int, error) {
	if value == "" {
		return 5, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > 10 {
		return 0, errors.New("VIEWPORT_BATCHES must be an integer from 1 to 10")
	}
	return n, nil
}

func viewportAPIBase(base string) error {
	if base != viewportBase {
		return errors.New("API URL must be exactly the disposable HTTP loopback forward")
	}
	return nil
}

func viewportURLs(selection generated.MapSelection, source string, tiles []viewportTile, now time.Time, limit int) ([]string, error) {
	if limit < 1 {
		return nil, errors.New("invalid worker limit")
	}
	// At concurrency one, 35 client timeouts need 1050 seconds. Never refresh an
	// immutable selection mid-batch; reserve the whole worst-case batch plus 6s.
	reserve := time.Duration((len(tiles)+limit-1)/limit)*30*time.Second + 6*time.Second
	if limit < 1 || selection.Id == "" || selection.DataGeneration < 1 || selection.ExpiresAt.Before(now.Add(reserve)) {
		return nil, errors.New("selection lacks generation/identity or sufficient immutable batch lifetime")
	}
	template := selection.RouteTileUrl
	if source == "coverage" {
		template = selection.CoverageTileUrl
	} else if source != "route" {
		return nil, errors.New("unknown private source")
	}
	// Validate the actual returned template, including its selection/generation,
	// without inventing a fallback when a response field is absent.
	expected := fmt.Sprintf("/api/map-selections/%s/%s-tiles/%d/{z}/{x}/{y}.pbf", selection.Id, source, selection.DataGeneration)
	if template != expected {
		return nil, errors.New("unexpected private tile URL template")
	}
	result := make([]string, 0, len(tiles))
	seen := map[string]bool{}
	for _, tile := range tiles {
		path := strings.NewReplacer("{z}", strconv.Itoa(tile.Z), "{x}", strconv.Itoa(tile.X), "{y}", strconv.Itoa(tile.Y)).Replace(template)
		u, err := url.Parse(viewportBase + path)
		if err != nil || u.Scheme != "http" || u.Host != "127.0.0.1:18081" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || seen[path] {
			return nil, errors.New("unsafe or duplicate private tile URL")
		}
		seen[path] = true
		result = append(result, u.String())
	}
	return result, nil
}

func viewportClient(limit int) *http.Client {
	return &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			Proxy: nil, ForceAttemptHTTP2: false,
			MaxConnsPerHost: limit, MaxIdleConns: limit, MaxIdleConnsPerHost: limit,
			IdleConnTimeout: 90 * time.Second, ResponseHeaderTimeout: 30 * time.Second,
			DialContext: (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
		},
	}
}

type viewportSample struct {
	Source       string       `json:"source"`
	Tile         viewportTile `json:"coord"`
	ScheduledAt  time.Time    `json:"scheduled_at"`
	DispatchedAt time.Time    `json:"dispatched_at"`
	BodyComplete time.Time    `json:"body_complete_at"`
	QueueMS      float64      `json:"queue_ms"`
	RequestMS    float64      `json:"request_ms"`
	ScheduledMS  float64      `json:"scheduled_to_body_ms"`
	Status       int          `json:"status"`
	Protocol     string       `json:"protocol"`
	Bytes        int          `json:"bytes"`
	Features     int          `json:"decoded_features"`
	ErrorClass   string       `json:"error_class"`
}

type viewportLatency struct {
	Samples int     `json:"samples"`
	P50MS   float64 `json:"p50_ms"`
	P95MS   float64 `json:"p95_ms"`
	MaxMS   float64 `json:"max_ms"`
}

func viewportPercentiles(values []float64) viewportLatency {
	if len(values) == 0 {
		return viewportLatency{}
	}
	v := append([]float64(nil), values...)
	sort.Float64s(v)
	return viewportLatency{len(v), v[int(math.Ceil(float64(len(v))*.5))-1], v[int(math.Ceil(float64(len(v))*.95))-1], v[len(v)-1]}
}

type viewportBatch struct {
	Index           int              `json:"batch"`
	Status          string           `json:"status"`
	FailureClass    string           `json:"failure_class"`
	StartedAt       time.Time        `json:"started_at"`
	FinishedAt      time.Time        `json:"finished_at"`
	CompletionMS    float64          `json:"viewport_completion_ms"`
	PeakActive      int              `json:"peak_offered_active_requests"`
	PeakConnected   int              `json:"peak_http1_connections"`
	Generation      int64            `json:"data_generation"`
	ExpiresAt       time.Time        `json:"selection_expires_at"`
	GenerationStart int64            `json:"generation_start"`
	GenerationEnd   int64            `json:"generation_end"`
	ActiveStart     int              `json:"active_children_start"`
	ActiveEnd       int              `json:"active_children_end"`
	SlotsStart      int              `json:"matcher_slots_start"`
	SlotsEnd        int              `json:"matcher_slots_end"`
	OtherJobsStart  int              `json:"active_other_jobs_start"`
	OtherJobsEnd    int              `json:"active_other_jobs_end"`
	Samples         []viewportSample `json:"samples"`
}

type viewportCaseBinding struct {
	SchemaVersion         int            `json:"schema_version"`
	Namespace             string         `json:"namespace"`
	NamespaceUID          string         `json:"namespace_uid"`
	Profile               string         `json:"profile"`
	ProfileVersion        int            `json:"profile_version"`
	ManifestSHA256        string         `json:"manifest_sha256"`
	ProfileManifestSHA256 string         `json:"profile_manifest_sha256"`
	Seed                  int64          `json:"seed"`
	Year                  int            `json:"year"`
	GPSSampleSeconds      int            `json:"gps_sample_seconds"`
	Provenance            map[string]any `json:"provenance"`
}

type viewportCase struct {
	viewportCaseBinding
	StartedAt      time.Time       `json:"started_at"`
	RecordedAt     time.Time       `json:"recorded_at"`
	Name           string          `json:"name"`
	Scope          string          `json:"scope"`
	Source         string          `json:"source"`
	Concurrency    int             `json:"concurrency"`
	Status         string          `json:"status"`
	RequestBudget  string          `json:"request_budget_status"`
	ViewportBudget string          `json:"viewport_budget_status"`
	Attempts       int             `json:"attempts"`
	Successes      int             `json:"successes"`
	Errors         int             `json:"errors"`
	Timeouts       int             `json:"timeouts"`
	FailureTotals  map[string]int  `json:"failure_totals"`
	Statuses       map[int]int     `json:"statuses"`
	Protocols      map[string]int  `json:"protocols"`
	Bytes          int             `json:"bytes"`
	Tiles          int             `json:"count_tiles"`
	FeatureTiles   int             `json:"feature_bearing_tiles"`
	EmptyTiles     int             `json:"empty_tiles"`
	FeatureCounts  int             `json:"feature_counts"`
	PeakActive     int             `json:"peak_offered_active_requests"`
	PeakConnected  int             `json:"peak_http1_connections"`
	Overall        viewportLatency `json:"overall"`
	FeatureBearing viewportLatency `json:"feature_bearing"`
	Empty          viewportLatency `json:"empty"`
	Completion     viewportLatency `json:"viewport_completion"`
	Batches        []viewportBatch `json:"batches"`
}

func viewportProvenance(input acceptanceProfileInput) map[string]any {
	metadata := input.metadata()
	metadata["kind"] = "captured_fenced_run"
	return metadata
}

func viewportNewCase(input acceptanceProfileInput, source string, limit int) viewportCase {
	m := input.metadata()
	return viewportCase{
		viewportCaseBinding: viewportCaseBinding{
			SchemaVersion: m["schema_version"].(int), Namespace: m["namespace"].(string), NamespaceUID: m["namespace_uid"].(string),
			Profile: m["profile"].(string), ProfileVersion: m["profile_version"].(int), ManifestSHA256: m["manifest_sha256"].(string),
			ProfileManifestSHA256: m["profile_manifest_sha256"].(string), Seed: m["seed"].(int64), Year: m["year"].(int), GPSSampleSeconds: m["gps_sample_seconds"].(int),
			Provenance: viewportProvenance(input),
		},
		Name: fmt.Sprintf("year_%s_c%d", source, limit), Scope: "year", Source: source, Concurrency: limit,
		StartedAt: time.Now().UTC(), Batches: []viewportBatch{},
	}
}

// Shared by the live driver and serialized Go/Node contract test. Keep the
// consumer-visible frame and binding in this one producer, not a test fixture.
func viewportArtifact(input acceptanceProfileInput, batches int) (map[string]any, []viewportTile, error) {
	artifact := input.metadata()
	artifact["status"], artifact["started_at"], artifact["account_count"] = "FAIL", time.Now().UTC(), 1
	artifact["provenance"] = viewportProvenance(input)
	tiles, center, err := viewportPlan(3095, 1973, .0195, .0195, 14)
	if err != nil {
		return artifact, nil, err
	}
	artifact["tile_plan"] = map[string]any{"css_width": 3095, "css_height": 1973, "tile_size": 512, "center_lng": .0195, "center_lat": .0195, "zoom": 14, "bearing": 0, "pitch": 0, "projection": "mercator", "center_tile": center, "tiles": tiles, "count": len(tiles), "order": "MapLibre squared distance to tile centers", "dpr_multiplies_css": false}
	artifact["configured_concurrency"], artifact["batches_per_case"], artifact["scope"] = []int{1, 6, 16, 35}, batches, "year"
	artifact["expected_measured_requests"], artifact["expected_preflight_tile_requests"] = 8*batches*len(tiles), 2*len(tiles)
	artifact["targets_ms"] = map[string]int{"request_p95_provisional": 500, "viewport_fetch_p95_proposed": 3000}
	artifact["warnings"] = []string{fmt.Sprintf("Viewport p95 uses only %d batches per case: coarse pilot evidence, not a stable SLO.", batches)}
	artifact["mvt_decoder"] = map[string]any{"layer_version": 2, "extent": 4096, "body_limit_bytes": 5 << 20, "value_types": []string{"string", "float", "double", "int", "uint", "sint", "bool"}, "sql_property_types_checked": true}
	artifact["measurement_notes"] = "One fixture account/session; route and coverage run separately (current UI has one active source). No application retry or selection refresh. HTTP/1.1; peak offered active includes transport queue, not backend concurrency; connection peak is GotConn through body completion. All requests, including failures, enter overall latency; feature/empty groups include validated successes only. Viewport completion is the batch critical path, never summed request p95. Proposed 3s fetch budget is not browser initial render. API upstream timeout is 5s (502 retained); client timeout is 30s. Local port-forward overhead included; DB/OS caches not flushed, sequential preflight warms tiles; full source geometry remains unchanged. Collector owns health/resource/telemetry overlap."
	return artifact, tiles, nil
}

func viewportFinishArtifact(artifact map[string]any, cases []viewportCase, probes []viewportBatch, failures map[string]int, recordedAt time.Time) {
	attempts, successes, requestErrors, timeouts, failedCases := 0, 0, 0, 0, 0
	requestFailures := map[string]int{}
	for _, c := range cases {
		attempts += c.Attempts
		successes += c.Successes
		requestErrors += c.Errors
		timeouts += c.Timeouts
		if c.Status != "PASS" {
			failedCases++
		}
		for k, n := range c.FailureTotals {
			requestFailures[k] += n
		}
		if c.FailureTotals["mvt_content"] > 0 {
			artifact["content_checks"].(map[string]any)["status"] = "FAIL"
		}
	}
	artifact["attempts"], artifact["successes"], artifact["errors"], artifact["timeouts"], artifact["failed_cases"] = attempts, successes, requestErrors, timeouts, failedCases
	artifact["request_failure_totals"] = requestFailures
	artifact["cases"], artifact["preflight"], artifact["failure_totals"], artifact["recorded_at"] = cases, probes, failures, recordedAt
}

// No testing methods run in workers. Results retain safe classes, never raw
// URLs, credentials, response bodies, or owner/selection/workout identifiers.
func viewportBurst(ctx context.Context, client *http.Client, token, source string, tiles []viewportTile, urls []string, limit int, validate func(viewportTile, []byte) (int, error)) viewportBatch {
	b := viewportBatch{Status: "PASS", StartedAt: time.Now().UTC(), Samples: []viewportSample{}}
	jobs := make(chan int, len(tiles))
	results := make(chan viewportSample, len(tiles))
	for i := range tiles {
		jobs <- i
	}
	close(jobs)
	var mu sync.Mutex
	active := 0
	connections := map[net.Conn]int{}
	var wg sync.WaitGroup
	for range min(limit, len(tiles)) {
		wg.Go(func() {
			for i := range jobs {
				s := viewportSample{Source: source, Tile: tiles[i], ScheduledAt: b.StartedAt}
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, urls[i], nil)
				if err != nil {
					s.ErrorClass = "request_setup"
					results <- s
					continue
				}
				req.Header.Set("Authorization", "Bearer "+token)
				var connection net.Conn
				req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
					mu.Lock()
					defer mu.Unlock()
					if connection != info.Conn {
						if connection != nil {
							connections[connection]--
							if connections[connection] == 0 {
								delete(connections, connection)
							}
						}
						connection = info.Conn
						connections[connection]++
						// A just-completed body can release a connection to another
						// worker before bookkeeping runs. Count physical identities,
						// not overlapping trace lifetimes on the reused connection.
						b.PeakConnected = max(b.PeakConnected, len(connections))
					}
				}}))
				mu.Lock()
				active++
				b.PeakActive = max(b.PeakActive, active)
				mu.Unlock()
				s.DispatchedAt = time.Now().UTC()
				resp, err := client.Do(req)
				var data []byte
				if resp != nil {
					s.Status, s.Protocol = resp.StatusCode, resp.Proto
					data, err = io.ReadAll(io.LimitReader(resp.Body, (5<<20)+1))
					closeErr := resp.Body.Close()
					if err == nil {
						err = closeErr
					}
					s.Bytes = len(data)
				}
				s.BodyComplete = time.Now().UTC()
				mu.Lock()
				active--
				if connection != nil {
					connections[connection]--
					if connections[connection] == 0 {
						delete(connections, connection)
					}
				}
				mu.Unlock()
				s.QueueMS = s.DispatchedAt.Sub(s.ScheduledAt).Seconds() * 1000
				s.RequestMS = s.BodyComplete.Sub(s.DispatchedAt).Seconds() * 1000
				s.ScheduledMS = s.BodyComplete.Sub(s.ScheduledAt).Seconds() * 1000
				switch {
				case err != nil:
					s.ErrorClass = viewportErrorClass(err)
				case s.Status == 404:
					s.ErrorClass = "unstable_selection_404"
				case s.Status != 200:
					s.ErrorClass = "http_status"
				case s.Protocol != "HTTP/1.1":
					s.ErrorClass = "unexpected_protocol"
				case len(data) > 5<<20:
					s.ErrorClass = "body_limit"
				default:
					s.Features, err = validate(s.Tile, data)
					if err != nil {
						s.ErrorClass = "mvt_content"
					}
				}
				results <- s
			}
		})
	}
	wg.Wait()
	close(results)
	for s := range results {
		b.Samples = append(b.Samples, s)
		b.CompletionMS = max(b.CompletionMS, s.ScheduledMS)
		if s.ErrorClass != "" {
			b.Status = "FAIL"
		}
		if s.ErrorClass == "unstable_selection_404" {
			b.FailureClass = "UNSTABLE"
		}
	}
	order := map[viewportTile]int{}
	for i, tile := range tiles {
		order[tile] = i
	}
	sort.Slice(b.Samples, func(i, j int) bool { return order[b.Samples[i].Tile] < order[b.Samples[j].Tile] })
	b.FinishedAt = time.Now().UTC()
	return b
}

func viewportErrorClass(err error) string {
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	return "transport_or_body"
}

func viewportAggregate(c *viewportCase) {
	c.RecordedAt = time.Now().UTC()
	c.Status, c.RequestBudget, c.ViewportBudget = "PASS", "PASS", "PASS"
	c.FailureTotals, c.Statuses, c.Protocols = map[string]int{}, map[int]int{}, map[string]int{}
	var overall, features, empty, completion []float64
	for _, b := range c.Batches {
		c.PeakActive, c.PeakConnected = max(c.PeakActive, b.PeakActive), max(c.PeakConnected, b.PeakConnected)
		if b.Status != "PASS" {
			c.Status = "FAIL"
		}
		if b.FailureClass != "" {
			c.FailureTotals[b.FailureClass]++
		}
		if len(b.Samples) > 0 {
			completion = append(completion, b.CompletionMS)
		}
		for _, s := range b.Samples {
			c.Attempts++
			c.Tiles++
			c.Bytes += s.Bytes
			c.Statuses[s.Status]++
			c.Protocols[s.Protocol]++
			overall = append(overall, s.RequestMS)
			if s.ErrorClass != "" {
				c.Errors++
				c.FailureTotals[s.ErrorClass]++
				if s.ErrorClass == "timeout" {
					c.Timeouts++
				}
				continue
			}
			c.Successes++
			c.FeatureCounts += s.Features
			if s.Features > 0 {
				c.FeatureTiles++
				features = append(features, s.RequestMS)
			} else {
				c.EmptyTiles++
				empty = append(empty, s.RequestMS)
			}
		}
	}
	c.Overall, c.FeatureBearing, c.Empty, c.Completion = viewportPercentiles(overall), viewportPercentiles(features), viewportPercentiles(empty), viewportPercentiles(completion)
	if c.Overall.Samples == 0 || c.Overall.P95MS > 500 {
		c.RequestBudget = "FAIL"
	}
	if c.Completion.Samples == 0 || c.Completion.P95MS > 3000 {
		c.ViewportBudget = "FAIL"
	}
	if c.Errors > 0 || c.RequestBudget == "FAIL" || c.ViewportBudget == "FAIL" {
		c.Status = "FAIL"
	}
}

type viewportField struct {
	number  protowire.Number
	kind    protowire.Type
	data    []byte
	integer uint64
}

func viewportFields(data []byte) ([]viewportField, error) {
	var fields []viewportField
	for len(data) > 0 {
		num, kind, n := protowire.ConsumeTag(data)
		if n < 0 || num < 1 {
			return nil, errors.New("invalid protobuf tag")
		}
		data = data[n:]
		f := viewportField{number: num, kind: kind}
		switch kind {
		case protowire.BytesType:
			f.data, n = protowire.ConsumeBytes(data)
		case protowire.VarintType:
			f.integer, n = protowire.ConsumeVarint(data)
		case protowire.Fixed32Type:
			var value uint32
			value, n = protowire.ConsumeFixed32(data)
			f.integer = uint64(value)
		case protowire.Fixed64Type:
			f.integer, n = protowire.ConsumeFixed64(data)
		default:
			return nil, errors.New("unsupported MVT wire type")
		}
		if n < 0 {
			return nil, errors.New("truncated protobuf value")
		}
		fields = append(fields, f)
		data = data[n:]
	}
	return fields, nil
}

type viewportFeature struct {
	Layer      string
	Properties map[string]any
}

// Decode the actual PostGIS MVT contract, including packed tags and geometry.
// A zero-byte tile and a legitimate empty layer are both zero-feature results.
func viewportDecodeMVT(tile []byte, source string) ([]viewportFeature, error) {
	root, err := viewportFields(tile)
	if err != nil {
		return nil, err
	}
	var result []viewportFeature
	for _, field := range root {
		if field.number != 3 || field.kind != protowire.BytesType {
			return nil, errors.New("invalid MVT root")
		}
		layer, err := viewportFields(field.data)
		if err != nil {
			return nil, err
		}
		name, version, extent := "", uint64(0), uint64(0)
		var keys []string
		var values []any
		var features [][]byte
		for _, f := range layer {
			switch {
			case f.number == 1 && f.kind == protowire.BytesType:
				name = string(f.data)
			case f.number == 2 && f.kind == protowire.BytesType:
				features = append(features, f.data)
			case f.number == 3 && f.kind == protowire.BytesType:
				keys = append(keys, string(f.data))
			case f.number == 4 && f.kind == protowire.BytesType:
				v, err := viewportFields(f.data)
				if err != nil || len(v) != 1 {
					return nil, errors.New("invalid MVT value")
				}
				switch {
				case v[0].number == 1 && v[0].kind == protowire.BytesType:
					values = append(values, string(v[0].data))
				case v[0].number == 2 && v[0].kind == protowire.Fixed32Type || v[0].number == 3 && v[0].kind == protowire.Fixed64Type:
					value := math.Float64frombits(v[0].integer)
					if v[0].number == 2 {
						value = float64(math.Float32frombits(uint32(v[0].integer)))
					}
					if math.IsNaN(value) || math.IsInf(value, 0) {
						return nil, errors.New("nonfinite MVT value")
					}
					values = append(values, value)
				case v[0].number == 4 && v[0].kind == protowire.VarintType:
					values = append(values, int64(v[0].integer))
				case v[0].number == 5 && v[0].kind == protowire.VarintType:
					if v[0].integer <= math.MaxInt64 {
						values = append(values, int64(v[0].integer))
					} else {
						values = append(values, v[0].integer)
					}
				case v[0].number == 6 && v[0].kind == protowire.VarintType:
					values = append(values, protowire.DecodeZigZag(v[0].integer))
				case v[0].number == 7 && v[0].kind == protowire.VarintType && v[0].integer <= 1:
					values = append(values, v[0].integer == 1)
				default:
					return nil, errors.New("unexpected MVT property type")
				}
			case f.number == 5 && f.kind == protowire.VarintType:
				extent = f.integer
			case f.number == 15 && f.kind == protowire.VarintType:
				version = f.integer
			default:
				return nil, errors.New("unexpected MVT layer field")
			}
		}
		if name == "" || version != 2 || extent != 4096 {
			return nil, errors.New("invalid MVT layer header")
		}
		if source == "route" && name != "routes" || source == "coverage" && name != "coverage" && name != "coverage_parks" && name != "coverage_focus" && name != "coverage_parks_focus" {
			return nil, errors.New("unexpected source layer")
		}
		for _, raw := range features {
			fs, err := viewportFields(raw)
			if err != nil {
				return nil, err
			}
			p := map[string]any{}
			geomType, geometry := uint64(0), []byte(nil)
			for _, f := range fs {
				switch {
				case f.number == 1 && f.kind == protowire.VarintType:
				case f.number == 2 && f.kind == protowire.BytesType:
					packed := f.data
					for len(packed) > 0 {
						k, n := protowire.ConsumeVarint(packed)
						if n < 0 {
							return nil, errors.New("invalid MVT key")
						}
						packed = packed[n:]
						v, n := protowire.ConsumeVarint(packed)
						if n < 0 || k >= uint64(len(keys)) || v >= uint64(len(values)) {
							return nil, errors.New("invalid MVT tag index")
						}
						packed = packed[n:]
						if _, exists := p[keys[k]]; exists {
							return nil, errors.New("duplicate MVT property")
						}
						p[keys[k]] = values[v]
					}
				case f.number == 3 && f.kind == protowire.VarintType:
					geomType = f.integer
				case f.number == 4 && f.kind == protowire.BytesType:
					geometry = f.data
				default:
					return nil, errors.New("invalid MVT feature field")
				}
			}
			if geomType < 1 || geomType > 3 || len(geometry) == 0 {
				return nil, errors.New("missing MVT geometry")
			}
			moved, points := false, 0
			for len(geometry) > 0 {
				command, n := protowire.ConsumeVarint(geometry)
				if n < 0 || command>>3 == 0 {
					return nil, errors.New("invalid MVT geometry command")
				}
				geometry = geometry[n:]
				id, count := command&7, command>>3
				if id == 7 && geomType == 3 && moved && count == 1 {
					continue
				}
				if (id != 1 && id != 2) || id == 2 && (!moved || geomType == 1) || count > uint64(len(geometry)/2) {
					return nil, errors.New("invalid MVT geometry sequence")
				}
				for range count * 2 {
					_, n := protowire.ConsumeVarint(geometry)
					if n < 0 {
						return nil, errors.New("invalid MVT coordinate")
					}
					geometry = geometry[n:]
				}
				moved = true
				points += int(count)
			}
			if !moved || geomType == 2 && points < 2 {
				return nil, errors.New("empty MVT feature geometry")
			}
			result = append(result, viewportFeature{name, p})
		}
	}
	return result, nil
}

type viewportOracle struct {
	Routes     map[string]generated.MapSelectionWorkout
	SortOrders map[string]int64
	Paths      map[string]map[string]any
	Tiles      map[string]map[viewportTile]map[string]bool
}

func (o viewportOracle) validate(source string, tile viewportTile, body []byte) (int, error) {
	features, err := viewportDecodeMVT(body, source)
	if err != nil {
		return 0, err
	}
	want := o.Tiles[source][tile]
	seen := map[string]bool{}
	for _, feature := range features {
		p := feature.Properties
		key := "entityId"
		if source == "route" {
			key = "workoutId"
		}
		id, ok := p[key].(string)
		if !ok || !want[id] || seen[id] {
			return len(features), errors.New("unexpected or duplicate MVT identity")
		}
		seen[id] = true
		if source == "route" {
			workout, ok := o.Routes[id]
			if !ok || feature.Layer != "routes" || p["workoutTypeKey"] != workout.Type.Key || p["workoutType"] != workout.Type.Name || p["sortOrder"] != o.SortOrders[id] {
				return len(features), errors.New("route provenance mismatch")
			}
		} else {
			if _, known := o.Paths[id]; !known || feature.Layer != "coverage" {
				return len(features), errors.New("unexpected coverage layer")
			}
			for k, v := range o.Paths[id] {
				if p[k] != v {
					return len(features), errors.New("coverage SQL provenance mismatch")
				}
			}
		}
	}
	if len(seen) != len(want) {
		return len(features), errors.New("MVT cardinality differs from SQL oracle")
	}
	return len(features), nil
}

// Independent read-only SQL over the fixture owner's full manifest year. The
// sparse 40x40 grid is not enlarged, densified, shortened, or capped.
func viewportLoadOracle(f *coverageIntegrationFixture, input acceptanceProfileInput, tiles []viewportTile) (viewportOracle, error) {
	o := viewportOracle{Routes: map[string]generated.MapSelectionWorkout{}, SortOrders: map[string]int64{}, Paths: map[string]map[string]any{}, Tiles: map[string]map[viewportTile]map[string]bool{"route": {}, "coverage": {}}}
	start, end := input.dateRange(0)
	tx := f.accountTx(f.ctx, f.apiDB)
	defer coverageIntegrationRollback(tx)
	rows, err := tx.Query(f.ctx, `SELECT upper(replace(w.id::text,'-','')),t.type_key,t.provider_label FROM app.workouts w
		JOIN app.workout_routes r ON r.account_id=w.account_id AND r.workout_id=w.id
		JOIN app.workout_types t ON t.account_id=w.account_id AND t.id=w.workout_type_id
		WHERE w.account_id=$1 AND w.local_start_date BETWEEN $2::date AND $3::date AND w.deletion_requested_at IS NULL AND r.route IS NOT NULL ORDER BY w.started_at,w.id`, f.accountID, start, end)
	if err != nil {
		return o, err
	}
	for rows.Next() {
		var id, key, label string
		if err := rows.Scan(&id, &key, &label); err != nil {
			rows.Close()
			return o, err
		}
		o.SortOrders[id] = int64(len(o.Routes))
		o.Routes[id] = generated.MapSelectionWorkout{Id: id, Type: generated.MapSelectionWorkoutType{Key: key, Name: label}}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return o, err
	}
	if len(o.Routes) != input.routedCount() {
		return o, errors.New("SQL routed cardinality differs from manifest")
	}
	rows, err = tx.Query(f.ctx, `SELECT upper(replace(m.logical_path_id::text,'-','')),min(p.name),min(p.locality_name),min(p.broad_class),min(m.region_id),count(DISTINCT w.id),app.coverage_count_bucket(count(DISTINCT w.id))
		FROM app.workout_segment_matches m JOIN app.workouts w ON w.account_id=m.account_id AND w.id=m.workout_id
		JOIN app.workout_routes r ON r.account_id=w.account_id AND r.workout_id=w.id
		JOIN app.workout_coverage_states s ON s.account_id=w.account_id AND s.workout_id=w.id
		JOIN app.coverage_paths p ON p.account_id=m.account_id AND p.logical_path_id=m.logical_path_id
		WHERE w.account_id=$1 AND w.local_start_date BETWEEN $2::date AND $3::date AND w.deletion_requested_at IS NULL AND r.route IS NOT NULL AND s.applied_route_input_revision IS NOT NULL
		GROUP BY m.logical_path_id`, f.accountID, start, end)
	if err != nil {
		return o, err
	}
	for rows.Next() {
		var id, name, locality, broad, region string
		var count, bucket int64
		if err := rows.Scan(&id, &name, &locality, &broad, &region, &count, &bucket); err != nil {
			rows.Close()
			return o, err
		}
		o.Paths[id] = map[string]any{"entityKind": "path", "name": name, "localityName": locality, "broadClass": broad, "regionId": region, "rangeWorkoutCount": count, "countBucket": bucket}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return o, err
	}
	if len(o.Paths) != 80 {
		return o, errors.New("full-year fixture must cover exactly 80 named grid paths")
	}
	names := map[string]bool{}
	for _, p := range o.Paths {
		name, _ := p["name"].(string)
		if names[name] || p["regionId"] != "fixture:dense" || p["broadClass"] != "road" || p["localityName"] != "Synthetic Acceptance City" {
			return o, errors.New("unexpected grid path metadata")
		}
		names[name] = true
	}
	for i := 1; i <= 40; i++ {
		if !names[fmt.Sprintf("Street %d", i)] || !names[fmt.Sprintf("Avenue %d", i)] {
			return o, errors.New("grid street/avenue names differ")
		}
	}
	for _, tile := range tiles {
		for _, source := range []string{"route", "coverage"} {
			o.Tiles[source][tile] = map[string]bool{}
			query := `WITH bounds AS (SELECT ST_TileEnvelope($4,$5,$6) geom)
				SELECT upper(replace(w.id::text,'-','')) FROM app.workouts w
				JOIN app.workout_routes r ON r.account_id=w.account_id AND r.workout_id=w.id CROSS JOIN bounds
				WHERE w.account_id=$1 AND w.local_start_date BETWEEN $2::date AND $3::date AND w.deletion_requested_at IS NULL AND r.route IS NOT NULL
				AND r.route && ST_Transform(bounds.geom,4326) AND ST_AsMVTGeom(ST_Transform(r.route,3857),bounds.geom::box2d,4096,64,true) IS NOT NULL`
			if source == "coverage" {
				query = `WITH bounds AS (SELECT ST_TileEnvelope($4,$5,$6) geom), paths AS (
					SELECT m.logical_path_id,ST_UnaryUnion(ST_Collect(m.geom)) geom FROM app.workout_segment_matches m
					JOIN app.workouts w ON w.account_id=m.account_id AND w.id=m.workout_id
					JOIN app.workout_routes r ON r.account_id=w.account_id AND r.workout_id=w.id
					JOIN app.workout_coverage_states s ON s.account_id=w.account_id AND s.workout_id=w.id CROSS JOIN bounds
					WHERE w.account_id=$1 AND w.local_start_date BETWEEN $2::date AND $3::date AND w.deletion_requested_at IS NULL AND r.route IS NOT NULL
					AND s.applied_route_input_revision IS NOT NULL AND m.geom && ST_Transform(bounds.geom,4326) GROUP BY m.logical_path_id)
					SELECT upper(replace(logical_path_id::text,'-','')) FROM paths CROSS JOIN bounds
					WHERE ST_Intersects(ST_Transform(paths.geom,3857),bounds.geom)
					AND ST_AsMVTGeom(ST_Transform(paths.geom,3857),bounds.geom::box2d,4096,64,true) IS NOT NULL`
			}
			rows, err = tx.Query(f.ctx, query, f.accountID, start, end, tile.Z, tile.X, tile.Y)
			if err != nil {
				return o, err
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return o, err
				}
				o.Tiles[source][tile][id] = true
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return o, err
			}
		}
	}
	return o, nil
}

type viewportState struct {
	Generation int64 `json:"data_generation"`
	Children   int   `json:"active_matching_children"`
	Slots      int   `json:"matcher_slots"`
	OtherJobs  int   `json:"active_other_jobs"`
}

func viewportSnapshot(f *coverageIntegrationFixture) (viewportState, error) {
	// Unlike the shared fixture's Fatal-based transaction helper, snapshots must
	// return cancellation/connection failures so later cases and artifacts survive.
	tx, err := f.adminDB.Begin(f.ctx)
	if err != nil {
		return viewportState{}, err
	}
	defer coverageIntegrationRollback(tx)
	if _, err := tx.Exec(f.ctx, `SELECT set_config('app.account_id',$1,true)`, f.accountID.String()); err != nil {
		return viewportState{}, err
	}
	var s viewportState
	err = tx.QueryRow(f.ctx, `SELECT (SELECT generation FROM app.account_data_generations WHERE account_id=$1),
		(SELECT count(*) FROM app.jobs WHERE kind='coverage_update_route' AND status IN ('queued','running')),
		(SELECT count(*) FROM app.matcher_slots),
		(SELECT count(*) FROM app.jobs WHERE kind<>'coverage_update_route' AND status IN ('queued','running'))`, f.accountID).Scan(&s.Generation, &s.Children, &s.Slots, &s.OtherJobs)
	return s, err
}

func viewportSelection(ctx context.Context, client *http.Client, token, body string, o viewportOracle) (generated.MapSelection, error) {
	var s generated.MapSelection
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, viewportBase+"/api/map-selections", strings.NewReader(body))
	if err != nil {
		return s, errors.New("selection_setup")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return s, fmt.Errorf("selection_%s", viewportErrorClass(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		return s, fmt.Errorf("selection_http_%d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return s, errors.New("selection_body")
	}
	if json.Unmarshal(data, &s) != nil {
		return s, errors.New("selection_decode")
	}
	seen := map[string]bool{}
	for index, w := range s.Workouts {
		want, ok := o.Routes[w.Id]
		if !ok || seen[w.Id] || o.SortOrders[w.Id] != int64(index) || w.Type.Key != want.Type.Key || w.Type.Name != want.Type.Name {
			return s, errors.New("selection_SQL_provenance")
		}
		seen[w.Id] = true
	}
	if len(seen) != len(o.Routes) {
		return s, errors.New("selection_SQL_cardinality")
	}
	return s, nil
}

// Node-facing contract: action viewport -> this exact test, schema v3 artifact
// input.artifact("viewport"), cases named year_{route,coverage}_c{1,6,16,35}.
// Samples are nested in cases[].batches[]; preflight is explicitly unmeasured.
func TestCoverageAcceptanceViewport(t *testing.T) {
	if os.Getenv("COVERAGE_ACCEPTANCE_ACTION") != "viewport" {
		t.Skip("viewport only")
	}
	f := acceptanceFixture(t)
	input := acceptanceLoadProfile(t)
	batches, batchErr := viewportBatchCount(os.Getenv("VIEWPORT_BATCHES"))
	artifact, tiles, planErr := viewportArtifact(input, batches)
	cases, probes := []viewportCase{}, []viewportBatch{}
	failures := map[string]int{}
	content := map[string]any{"status": "NOT_RUN"}
	artifact["content_checks"] = content
	// Installed early so setup/invariant failures also leave profile-bound evidence.
	defer func() {
		viewportFinishArtifact(artifact, cases, probes, failures, time.Now().UTC())
		data, err := json.MarshalIndent(artifact, "", "  ")
		if err == nil {
			err = os.WriteFile(input.artifact("viewport"), append(data, '\n'), 0600)
		}
		if err != nil {
			t.Errorf("write viewport artifact: %v", err)
		}
		if artifact["status"] != "PASS" {
			t.Errorf("viewport acceptance FAIL; safe failure totals=%v; artifact=%s", failures, input.artifact("viewport"))
		}
	}()
	refuse := func(class string) { failures[class]++; t.Logf("viewport refused: %s", class) }
	if viewportAPIBase(os.Getenv("COVERAGE_ACCEPTANCE_API_URL")) != nil {
		refuse("api_origin_guard")
		return
	}
	if path := os.Getenv("COVERAGE_ACCEPTANCE_RESULTS"); path != "" && path != input.artifact("viewport") {
		refuse("artifact_path_guard")
		return
	}
	if batchErr != nil {
		refuse("batch_count_guard")
		return
	}
	if planErr != nil || len(tiles) != 35 {
		refuse("tile_plan")
		return
	}
	uid := os.Getenv("COVERAGE_ACCEPTANCE_NAMESPACE_UID")
	if _, err := uuid.Parse(uid); err != nil {
		refuse("namespace_uid_guard")
		return
	}
	var binding struct {
		Profile   string `json:"profile"`
		Version   int    `json:"profile_version"`
		Hash      string `json:"profile_manifest_sha256"`
		UID       string `json:"namespace_uid"`
		CreatedAt string `json:"namespace_created_at"`
	}
	raw, err := exec.CommandContext(f.ctx, "node", "-e", `const p=require("../deploy/acceptance/profile.cjs");const ns=p.requireProfile();process.stdout.write(JSON.stringify({...p.binding,namespace_uid:ns.metadata.uid,namespace_created_at:ns.metadata.creationTimestamp}));`).Output()
	if err != nil || json.Unmarshal(raw, &binding) != nil || binding.Profile != input.Name || binding.Version != input.Manifest.SchemaVersion || binding.Hash != input.SHA256 || binding.UID != uid {
		refuse("live_profile_marker_guard")
		return
	}
	artifact["namespace_created_at"] = binding.CreatedAt
	f.accountID = acceptanceID("account", 0)
	tx := f.accountTx(f.ctx, f.adminDB)
	var owner bool
	err = tx.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM app.accounts a JOIN app.users u ON u.account_id=a.id
		JOIN app.authentication_principals p ON p.id=u.principal_id JOIN app.sources s ON s.account_id=a.id
		JOIN app.sessions session ON session.principal_id=p.id WHERE a.id=$1 AND p.username='acceptance-fixture'
		AND s.id=$2 AND s.generation=1 AND session.id=$3 AND session.revoked_at IS NULL AND session.expires_at>clock_timestamp()+interval '18 minutes')`, f.accountID, acceptanceID("source", 0), acceptanceID("session", 0)).Scan(&owner)
	coverageIntegrationRollback(tx)
	if err != nil || !owner {
		refuse("fixture_owner_session_guard")
		return
	}
	roles := []map[string]any{}
	for _, pool := range []*pgxpool.Pool{f.adminDB, f.apiDB, f.workerDB, f.coverageDB} {
		var role string
		var super, bypass bool
		if err := pool.QueryRow(f.ctx, `SELECT current_user,rolsuper,rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(&role, &super, &bypass); err != nil {
			refuse("role_snapshot")
			return
		}
		roles = append(roles, map[string]any{"role": role, "superuser": super, "bypass_rls": bypass})
		if super || bypass {
			refuse("role_privilege_guard")
			return
		}
	}
	artifact["roles"] = roles
	// Collector owns resource/telemetry window correlation; retain only container
	// resources here, not pod names or configuration that could contain secrets.
	raw, err = exec.CommandContext(f.ctx, "kubectl", "--context", "xdev", "-n", "workouts-acceptance", "get", "deployments", "-o", "json").Output()
	var deployments struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Template struct {
					Spec struct {
						Containers []struct {
							Name      string         `json:"name"`
							Resources map[string]any `json:"resources"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err != nil || json.Unmarshal(raw, &deployments) != nil {
		refuse("resource_snapshot")
		return
	}
	resources := map[string]any{}
	for _, d := range deployments.Items {
		for _, c := range d.Spec.Template.Spec.Containers {
			resources[d.Metadata.Name+"/"+c.Name] = c.Resources
		}
	}
	artifact["resources"], artifact["collector_phase"] = resources, "viewport"
	state, err := viewportSnapshot(f)
	artifact["state_start"] = state
	if err != nil || state.Children != 0 || state.Slots != 0 {
		refuse("matching_idle_guard")
		return
	}
	grid := acceptanceLoadGrid(f)
	artifact["canonical_grid_edges"] = len(grid)
	oracle, err := viewportLoadOracle(f, input, tiles)
	if err != nil {
		refuse("SQL_oracle")
		return
	}
	content["status"], content["grid_size"], content["named_paths_sql_oracle"], content["routed_workouts_sql_oracle"], content["per_tile_SQL_cardinality"] = "FAIL", 40, len(oracle.Paths), len(oracle.Routes), true
	digest := sha256.Sum256([]byte("FAKE disposable synthetic coverage acceptance bearer; never a live credential"))
	token := base64.RawURLEncoding.EncodeToString(digest[:])
	start, end := input.dateRange(0)
	body := fmt.Sprintf(`{"startDate":%q,"endDate":%q}`, start, end)
	control := viewportClient(1)
	defer control.CloseIdleConnections()
	// Exactly 70 extra tile probes, outside all measured cases. Each source gets
	// its own immutable selection with a full serial-timeout lifetime reserve.
	preflightOK := true
	for _, source := range []string{"route", "coverage"} {
		selection, err := viewportSelection(f.ctx, control, token, body, oracle)
		if err != nil {
			refuse(err.Error())
			return
		}
		if selection.DataGeneration != state.Generation {
			refuse("preflight_UNSTABLE_generation")
			return
		}
		urls, err := viewportURLs(selection, source, tiles, time.Now(), 1)
		if err != nil {
			refuse("preflight_selection_lifetime")
			return
		}
		probe := viewportBurst(f.ctx, control, token, source, tiles, urls, 1, func(tile viewportTile, data []byte) (int, error) { return oracle.validate(source, tile, data) })
		probe.Generation, probe.ExpiresAt = selection.DataGeneration, selection.ExpiresAt
		positive := 0
		for _, s := range probe.Samples {
			if s.ErrorClass != "" {
				preflightOK = false
				failures["preflight_"+s.ErrorClass]++
			}
			if s.Features > 0 && s.ErrorClass == "" {
				positive++
			}
		}
		if positive == 0 {
			probe.Status = "FAIL"
			preflightOK = false
			failures["preflight_no_features"]++
		}
		probes = append(probes, probe)
	}
	if !preflightOK {
		refuse("preflight_content")
		return
	}
	content["status"] = "PASS"
	for _, source := range []string{"route", "coverage"} {
		for _, limit := range []int{1, 6, 16, 35} {
			c := viewportNewCase(input, source, limit)
			client := viewportClient(limit)
			for batch := 1; batch <= batches; batch++ {
				b := viewportBatch{Index: batch, Status: "FAIL", Samples: []viewportSample{}}
				before, snapErr := viewportSnapshot(f)
				b.GenerationStart, b.ActiveStart, b.SlotsStart = before.Generation, before.Children, before.Slots
				b.OtherJobsStart = before.OtherJobs
				if snapErr != nil || before.Generation != state.Generation || before.Children != 0 || before.Slots != 0 {
					b.FailureClass = "UNSTABLE"
					c.Batches = append(c.Batches, b)
					continue
				}
				selection, selErr := viewportSelection(f.ctx, control, token, body, oracle)
				if selErr != nil {
					b.FailureClass = selErr.Error()
					c.Batches = append(c.Batches, b)
					continue
				}
				urls, urlErr := viewportURLs(selection, source, tiles, time.Now(), limit)
				if urlErr != nil || selection.DataGeneration != state.Generation {
					b.FailureClass = "selection_lifetime_or_generation"
					c.Batches = append(c.Batches, b)
					continue
				}
				b = viewportBurst(f.ctx, client, token, source, tiles, urls, limit, func(tile viewportTile, data []byte) (int, error) { return oracle.validate(source, tile, data) })
				b.Index, b.Generation, b.GenerationStart, b.ActiveStart, b.SlotsStart = batch, selection.DataGeneration, before.Generation, before.Children, before.Slots
				b.ExpiresAt, b.OtherJobsStart = selection.ExpiresAt, before.OtherJobs
				after, snapErr := viewportSnapshot(f)
				b.GenerationEnd, b.ActiveEnd, b.SlotsEnd = after.Generation, after.Children, after.Slots
				b.OtherJobsEnd = after.OtherJobs
				if snapErr != nil || after.Generation != state.Generation || after.Children != 0 || after.Slots != 0 {
					b.Status, b.FailureClass = "FAIL", "UNSTABLE"
				}
				c.Batches = append(c.Batches, b)
				if b.FailureClass == "UNSTABLE" {
					t.Logf("%s batch=%d UNSTABLE; no refresh/retry", c.Name, batch)
				}
			}
			client.CloseIdleConnections()
			viewportAggregate(&c)
			if c.Attempts != batches*len(tiles) {
				c.Status = "FAIL"
				c.FailureTotals["missing_attempts"]++
			}
			if c.Status != "PASS" {
				failures[c.Name]++
			}
			cases = append(cases, c)
			t.Logf("%s status=%s attempts=%d errors=%d peak=%d connections=%d request_p95=%.1fms viewport_p95=%.1fms", c.Name, c.Status, c.Attempts, c.Errors, c.PeakActive, c.PeakConnected, c.Overall.P95MS, c.Completion.P95MS)
		}
	}
	final, err := viewportSnapshot(f)
	artifact["state_end"] = final
	if err != nil || final.Generation != state.Generation || final.Children != 0 || final.Slots != 0 {
		failures["final_immutable_guard"]++
	}
	if len(failures) == 0 && len(cases) == 8 {
		artifact["status"] = "PASS"
	}
}
