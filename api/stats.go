package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const maximumStatsResponseBytes = 4 << 20

var errStatsResponseLimit = errors.New("report page exceeds 4 MiB; reduce rows per page")

func (s *Server) CreateStatsReportScope(w http.ResponseWriter, r *http.Request, params generated.CreateStatsReportScopeParams) {
	session, ok := s.requireSession(w, r, "user")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	var input generated.StatsReportScopeCreate
	if !decodeJSON(w, r, &input) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	tx, err := s.statsTransaction(ctx, session, false)
	if err != nil {
		writeStatsError(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	prefs, err := queryReadPreferences(ctx, tx, *session.accountID)
	if err != nil {
		writeStatsError(w, r, err)
		return
	}
	resolved, field, err := resolveAccountRange(ctx, tx, rangeInput{input.StartDate, input.EndDate, input.DateRangeEnum, input.Tz}, prefs, time.Now())
	if err != nil {
		if field == "" {
			writeStatsError(w, r, err)
			return
		}
		writeWorkoutRangeError(w, r, field, err.Error())
		return
	}
	if resolved.start.Year() < 1 || resolved.end.Year() > 9999 {
		writeWorkoutRangeError(w, r, "startDate", "dates must be within years 0001 through 9999")
		return
	}
	var generation int64
	if err := tx.QueryRow(ctx, `SELECT app.lock_account_data_generation()`).Scan(&generation); err != nil {
		writeStatsError(w, r, err)
		return
	}
	id, err := uuid.NewV7()
	if err != nil {
		writeStatsError(w, r, err)
		return
	}
	expiry := time.Now().UTC().Add(mapSelectionLifetime)
	if session.expiresAt.Before(expiry) {
		expiry = session.expiresAt
	}
	var contextID uuid.UUID
	var revision int64
	var policy string
	if err := tx.QueryRow(ctx, `SELECT * FROM app.stats_prepare_report_scope($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		*session.accountID, session.sessionID, id, generation, resolved.start, resolved.end, resolved.timezone, expiry, resolved.allTime).
		Scan(&contextID, &revision, &policy); err != nil {
		writeStatsError(w, r, err)
		return
	}
	result, err := readStatsReportScope(ctx, tx, *session.accountID, session.sessionID, id, generation)
	if err != nil || tx.Commit(ctx) != nil {
		writeStatsError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/stats/report-scopes/"+result.Id)
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) DeleteStatsReportScope(w http.ResponseWriter, r *http.Request, reportScopeID generated.StatsReportScopeID, params generated.DeleteStatsReportScopeParams) {
	session, ok := s.requireSession(w, r, "user")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	id, valid := parseCompactUUID(string(reportScopeID))
	if !valid {
		writeFieldError(w, r, "reportScopeId", "format", "report scope ID is invalid")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tx, err := s.statsTransaction(ctx, session, false)
	if err != nil {
		writeStatsError(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT app.stats_delete_report_scope($1,$2,$3)`, *session.accountID, session.sessionID, id); err != nil || tx.Commit(ctx) != nil {
		writeStatsError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) statsTransaction(ctx context.Context, session authenticatedSession, readOnly bool) (pgx.Tx, error) {
	options := pgx.TxOptions{IsoLevel: pgx.RepeatableRead}
	if readOnly {
		options.AccessMode = pgx.ReadOnly
	}
	tx, err := s.sessionAccountTransactionWithOptions(ctx, session, options)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='25s'; SET LOCAL lock_timeout='500ms'; SET LOCAL jit=off`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func readStatsReportScope(ctx context.Context, tx pgx.Tx, accountID, sessionID, id uuid.UUID, generation int64) (generated.StatsReportScope, error) {
	result := generated.StatsReportScope{Id: compactUUID(id), DataGeneration: generation}
	err := tx.QueryRow(ctx, `SELECT * FROM app.stats_read_report_scope($1,$2,$3,$4)`, accountID, sessionID, id, generation).
		Scan(&result.Range.StartDate.Time, &result.Range.EndDate.Time, &result.Range.Timezone, &result.ExpiresAt,
			&result.CoverageDisplayRevision, &result.CoverageDisplayPolicy)
	return result, err
}

func statsGrouping(value *generated.StatsGrouping) (generated.StatsGrouping, bool) {
	if value == nil {
		return generated.Month, true
	}
	return *value, *value == generated.Month || *value == generated.Year
}

func statsOrder(raw *string, fields map[string]string, defaultField, ties string) (string, error) {
	value := defaultField + ":desc"
	if raw != nil {
		value = *raw
	}
	parts := strings.Split(value, ":")
	if len(parts) != 2 || fields[parts[0]] == "" || parts[1] != "asc" && parts[1] != "desc" {
		return "", fmt.Errorf("sort must be an allowlisted field:asc or field:desc value")
	}
	return fields[parts[0]] + " " + parts[1] + " NULLS LAST," + ties, nil
}

func (s *Server) statsPagination(page, size *int) (int, int, bool) {
	p, n := 1, 25
	if page != nil {
		p = *page
	}
	if size != nil {
		n = *size
	}
	maximum := 100
	if s.config.PageSizeMaximum > 0 && s.config.PageSizeMaximum < maximum {
		maximum = s.config.PageSizeMaximum
	}
	// Keep offset arithmetic bounded even for callers bypassing OpenAPI validation.
	return p, n, p > 0 && p <= 2147483647 && n > 0 && n <= maximum
}

func (s *Server) ListNewCoverageBuckets(w http.ResponseWriter, r *http.Request, reportScopeID generated.StatsReportScopeID, params generated.ListNewCoverageBucketsParams) {
	session, ok := s.requireSession(w, r, "user")
	if !ok {
		return
	}
	id, valid := parseCompactUUID(string(reportScopeID))
	group, groupValid := statsGrouping(params.GroupBy)
	page, size, pageValid := s.statsPagination(params.Page, params.PageSize)
	order, err := statsOrder(params.Sort, map[string]string{"bucket": "bucket_start", "allTime": "all_time", "pastYear": "past_year"}, "bucket", "bucket_start DESC")
	if !valid || params.Generation < 1 || !groupValid || !pageValid || err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "report identifier, grouping, pagination or sort is invalid")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tx, err := s.statsTransaction(ctx, session, true)
	if err != nil {
		writeStatsError(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	scope, err := readStatsReportScope(ctx, tx, *session.accountID, session.sessionID, id, params.Generation)
	if err != nil {
		writeStatsError(w, r, err)
		return
	}
	result, err := queryNewCoverageBuckets(ctx, tx, *session.accountID, session.sessionID, id, scope, group, page, size, order)
	if err != nil || tx.Commit(ctx) != nil {
		writeStatsError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, result)
}

func queryNewCoverageBuckets(ctx context.Context, tx pgx.Tx, accountID, sessionID, id uuid.UUID, scope generated.StatsReportScope, group generated.StatsGrouping, page, size int, order string) (generated.NewCoverageBucketList, error) {
	var body []byte
	err := tx.QueryRow(ctx, `WITH events AS MATERIALIZED (
		SELECT * FROM app.stats_new_coverage_events($1,$2,$3,$4)
	), counts AS (
		SELECT date_trunc($5,new_visit::timestamp)::date bucket_start,
			count(*) FILTER (WHERE all_time_new) all_time,count(*) past_year FROM events GROUP BY 1
	), buckets AS MATERIALIZED (
		SELECT bucket::date bucket_start,COALESCE(counts.all_time,0) all_time,COALESCE(counts.past_year,0) past_year
		FROM generate_series(date_trunc($5,$6::date::timestamp),date_trunc($5,$7::date::timestamp),
			CASE WHEN $5='month' THEN interval '1 month' ELSE interval '1 year' END) bucket
		LEFT JOIN counts ON counts.bucket_start=bucket::date
	)
	SELECT jsonb_build_object('total',(SELECT count(*) FROM buckets),'items',COALESCE((
		SELECT jsonb_agg(jsonb_build_object('bucketStart',bucket_start,'allTime',all_time,'pastYear',past_year))
		FROM (SELECT * FROM buckets ORDER BY `+order+` LIMIT $8 OFFSET $9) page
	),'[]'::jsonb))`, accountID, sessionID, id, scope.DataGeneration, string(group), scope.Range.StartDate.Time,
		scope.Range.EndDate.Time, size, int64(page-1)*int64(size)).Scan(&body)
	result := generated.NewCoverageBucketList{Scope: scope, GroupBy: group}
	if err != nil {
		return result, err
	}
	var data struct {
		Total int64                         `json:"total"`
		Items []generated.NewCoverageBucket `json:"items"`
	}
	if err := decodeStatsPage(body, &data); err != nil {
		return result, err
	}
	result.Items, result.Pagination = data.Items, pagination(page, size, data.Total)
	return result, nil
}

func statsBucketEnd(start time.Time, group generated.StatsGrouping) time.Time {
	if group == generated.Year {
		return start.AddDate(1, 0, 0).AddDate(0, 0, -1)
	}
	return start.AddDate(0, 1, 0).AddDate(0, 0, -1)
}

func validStatsBucket(start time.Time, group generated.StatsGrouping, scope generated.StatsReportScope) bool {
	return start.Year() >= 1 && start.Year() <= 9999 && start.Day() == 1 &&
		(group != generated.Year || start.Month() == time.January) &&
		!start.After(scope.Range.EndDate.Time) && !statsBucketEnd(start, group).Before(scope.Range.StartDate.Time)
}

func (s *Server) ListNewCoverageVisits(w http.ResponseWriter, r *http.Request, reportScopeID generated.StatsReportScopeID, params generated.ListNewCoverageVisitsParams) {
	session, ok := s.requireSession(w, r, "user")
	if !ok {
		return
	}
	id, valid := parseCompactUUID(string(reportScopeID))
	group, groupValid := statsGrouping(params.GroupBy)
	page, size, pageValid := s.statsPagination(params.Page, params.PageSize)
	order, err := statsOrder(params.Sort, map[string]string{"name": "name", "place": "place", "newVisit": "new_visit", "previousVisit": "previous_visit"}, "newVisit", `name COLLATE "C",place COLLATE "C",entity_id`)
	if !valid || params.Generation < 1 || !groupValid || !pageValid || err != nil ||
		params.Metric != generated.AllTime && params.Metric != generated.PastYear || params.Search != nil && len([]rune(*params.Search)) > 256 {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "report identifier, grouping, metric, search, pagination or sort is invalid")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tx, err := s.statsTransaction(ctx, session, true)
	if err != nil {
		writeStatsError(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	scope, err := readStatsReportScope(ctx, tx, *session.accountID, session.sessionID, id, params.Generation)
	if err != nil {
		writeStatsError(w, r, err)
		return
	}
	if !validStatsBucket(params.BucketStart.Time, group, scope) {
		writeFieldError(w, r, "bucketStart", "range", "bucket must be aligned and intersect the report range")
		return
	}
	result, err := queryNewCoverageVisits(ctx, tx, *session.accountID, session.sessionID, id, scope, group, params, page, size, order)
	if err != nil || tx.Commit(ctx) != nil {
		writeStatsError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, result)
}

func queryNewCoverageVisits(ctx context.Context, tx pgx.Tx, accountID, sessionID, id uuid.UUID, scope generated.StatsReportScope, group generated.StatsGrouping, params generated.ListNewCoverageVisitsParams, page, size int, order string) (generated.NewCoverageVisitList, error) {
	search := ""
	if params.Search != nil {
		search = *params.Search
	}
	var body []byte
	err := tx.QueryRow(ctx, `WITH events AS MATERIALIZED (
		SELECT entity_id,entity_kind,name,new_visit,previous_visit,
			COALESCE(locality_name,region_name,initcap(regexp_replace(regexp_replace(region_id,'^.*:',''),'[-_]+',' ','g'))) place
		FROM app.stats_new_coverage_events($1,$2,$3,$4)
		WHERE new_visit BETWEEN $5 AND $6 AND ($7='past_year' OR all_time_new)
	), filtered AS MATERIALIZED (
		SELECT * FROM events WHERE $8='' OR strpos(
			normalize(casefold(normalize(name||' '||place,NFC) COLLATE pg_catalog.pg_c_utf8),NFC),
			normalize(casefold(normalize($8,NFC) COLLATE pg_catalog.pg_c_utf8),NFC))>0
	)
	SELECT jsonb_build_object('totalVisits',(SELECT count(*) FROM events),'totalFiltered',(SELECT count(*) FROM filtered),
		'items',COALESCE((SELECT jsonb_agg(jsonb_build_object('entityId',replace(upper(entity_id::text),'-',''),
			'entityKind',entity_kind,'name',name,'place',place,'newVisit',new_visit,'previousVisit',previous_visit,
			'newVisitWorkoutId',replace(upper(navigation.new_workout_id::text),'-',''),
			'previousVisitWorkoutId',replace(upper(navigation.previous_workout_id::text),'-','')))
			FROM (SELECT * FROM filtered ORDER BY `+order+` LIMIT $9 OFFSET $10) page
			CROSS JOIN LATERAL app.stats_visit_workouts($1,$2,$3,$4,page.entity_id,page.new_visit,page.previous_visit) navigation),'[]'::jsonb))`,
		accountID, sessionID, id, scope.DataGeneration, params.BucketStart.Time, statsBucketEnd(params.BucketStart.Time, group),
		string(params.Metric), search, size, int64(page-1)*int64(size)).Scan(&body)
	result := generated.NewCoverageVisitList{Scope: scope, GroupBy: group, BucketStart: params.BucketStart, Metric: params.Metric}
	if err != nil {
		return result, err
	}
	var data struct {
		TotalVisits   int64                        `json:"totalVisits"`
		TotalFiltered int64                        `json:"totalFiltered"`
		Items         []generated.NewCoverageVisit `json:"items"`
	}
	if err := decodeStatsPage(body, &data); err != nil {
		return result, err
	}
	result.Items, result.TotalVisits, result.Pagination = data.Items, data.TotalVisits, pagination(page, size, data.TotalFiltered)
	return result, nil
}

func decodeStatsPage(body []byte, value any) error {
	if len(body) > maximumStatsResponseBytes {
		return errStatsResponseLimit
	}
	return json.Unmarshal(body, value)
}

func writeStatsError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, http.StatusNotFound, "Not Found", "report scope is unavailable; refresh the report")
	} else if errors.Is(err, errStatsResponseLimit) {
		writeValidationProblem(w, r, http.StatusUnprocessableEntity, err.Error(), generated.ValidationError{Field: "pageSize", Code: "limit"})
	} else {
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "statistics are temporarily unavailable")
	}
}
