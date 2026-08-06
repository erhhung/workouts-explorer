package api

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

type readPreferences struct {
	timezone     string
	firstWeekday string
	pageSize     int
}

type resolvedRange struct {
	start, end time.Time
	timezone   string
}

type rangeInput struct {
	start, end *openapi_types.Date
	shortcut   *generated.DateRangeEnum
	timezone   *string
}

type workoutSort struct {
	field, direction string
}

var workoutSortExpressions = map[string]string{
	"date":      "w.started_at",
	"type":      "wt.provider_label COLLATE \"C\"",
	"duration":  "w.provider_duration",
	"distance":  "metrics.distance_value",
	"pace":      "CASE WHEN metrics.speed_value > 0 THEN 60 / metrics.speed_value END",
	"calories":  "metrics.energy_value",
	"heartRate": "metrics.heart_rate_value",
	"elevation": "metrics.elevation_value",
}

func (s *Server) ListWorkouts(w http.ResponseWriter, r *http.Request, params generated.ListWorkoutsParams) {
	session, ok := s.requireSession(w, r, "user")
	if !ok {
		return
	}
	tx, err := s.accountTransactionWithOptions(r.Context(), *session.accountID, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		writeWorkoutUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	prefs, err := queryReadPreferences(r.Context(), tx, *session.accountID)
	if err != nil {
		writeWorkoutUnavailable(w, r)
		return
	}
	zone := (*string)(params.Tz)
	resolved, field, err := resolveRange(rangeInput{params.StartDate, params.EndDate, params.DateRangeEnum, zone}, prefs, time.Now())
	if err != nil {
		writeWorkoutRangeError(w, r, field, err.Error())
		return
	}
	page := 1
	if params.Page != nil {
		page = *params.Page
	}
	pageSize := prefs.pageSize
	maximum := s.config.PageSizeMaximum
	if maximum == 0 {
		maximum = 100
	}
	if params.PageSize != nil {
		pageSize = *params.PageSize
	} else if pageSize > maximum {
		pageSize = maximum
	}
	if page < 1 {
		writeWorkoutFieldError(w, r, "page", "must be at least 1")
		return
	}
	if pageSize < 1 || pageSize > maximum {
		writeWorkoutFieldError(w, r, "pageSize", fmt.Sprintf("must be between 1 and %d", maximum))
		return
	}
	if page > math.MaxInt/pageSize {
		writeWorkoutFieldError(w, r, "page", "is too large")
		return
	}
	sorts, err := parseWorkoutSort(params.Sort)
	if err != nil {
		writeWorkoutFieldError(w, r, "sort", err.Error())
		return
	}

	var total int64
	if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM app.workouts WHERE local_start_date BETWEEN $1 AND $2`, resolved.start, resolved.end).Scan(&total); err != nil {
		writeWorkoutUnavailable(w, r)
		return
	}
	items, err := queryWorkouts(r.Context(), tx, resolved, page, pageSize, sorts)
	if err != nil {
		writeWorkoutUnavailable(w, r)
		return
	}
	totalPages := int64(0)
	if total > 0 {
		totalPages = (total + int64(pageSize) - 1) / int64(pageSize)
	}
	writeJSON(w, http.StatusOK, generated.WorkoutList{
		Range:      rangeResponse(resolved),
		Pagination: generated.Pagination{Page: page, PageSize: pageSize, TotalItems: total, TotalPages: totalPages},
		Items:      items,
	})
}

func (s *Server) ListWorkoutTypes(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r, "user")
	if !ok {
		return
	}
	tx, err := s.accountTransaction(r.Context(), *session.accountID)
	if err != nil {
		writeWorkoutUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	rows, err := tx.Query(r.Context(), `SELECT id,type_key,provider_label FROM app.workout_types ORDER BY provider_label COLLATE "C",type_key,id`)
	if err != nil {
		writeWorkoutUnavailable(w, r)
		return
	}
	defer rows.Close()
	result := generated.WorkoutTypeList{Items: []generated.WorkoutType{}}
	for rows.Next() {
		var id uuid.UUID
		var item generated.WorkoutType
		if err := rows.Scan(&id, &item.Key, &item.DisplayName); err != nil {
			writeWorkoutUnavailable(w, r)
			return
		}
		item.Id = compactUUID(id)
		result.Items = append(result.Items, item)
	}
	if rows.Err() != nil {
		writeWorkoutUnavailable(w, r)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) GetWorkoutSummary(w http.ResponseWriter, r *http.Request, params generated.GetWorkoutSummaryParams) {
	session, ok := s.requireSession(w, r, "user")
	if !ok {
		return
	}
	tx, err := s.accountTransaction(r.Context(), *session.accountID)
	if err != nil {
		writeWorkoutUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	prefs, err := queryReadPreferences(r.Context(), tx, *session.accountID)
	if err != nil {
		writeWorkoutUnavailable(w, r)
		return
	}
	zone := (*string)(params.Tz)
	resolved, field, err := resolveRange(rangeInput{params.StartDate, params.EndDate, params.DateRangeEnum, zone}, prefs, time.Now())
	if err != nil {
		writeWorkoutRangeError(w, r, field, err.Error())
		return
	}
	byType, totals, err := querySummary(r.Context(), tx, resolved)
	if err != nil {
		writeWorkoutUnavailable(w, r)
		return
	}
	writeJSON(w, http.StatusOK, generated.WorkoutSummary{Range: rangeResponse(resolved), Totals: totals, ByType: byType})
}

func queryReadPreferences(ctx context.Context, tx pgx.Tx, accountID uuid.UUID) (readPreferences, error) {
	var value readPreferences
	err := tx.QueryRow(ctx, `SELECT timezone,first_weekday,page_size FROM app.preferences WHERE account_id=$1`, accountID).Scan(&value.timezone, &value.firstWeekday, &value.pageSize)
	return value, err
}

func resolveRange(input rangeInput, prefs readPreferences, now time.Time) (resolvedRange, string, error) {
	explicit := input.start != nil || input.end != nil
	if explicit && input.shortcut != nil {
		return resolvedRange{}, "dateRangeEnum", fmt.Errorf("cannot be combined with explicit dates")
	}
	if explicit {
		if input.start == nil || input.end == nil {
			return resolvedRange{}, "startDate", fmt.Errorf("startDate and endDate must appear together")
		}
		start, end := input.start.Time, input.end.Time
		if start.After(end) {
			return resolvedRange{}, "endDate", fmt.Errorf("must not be before startDate")
		}
		if _, err := time.LoadLocation(prefs.timezone); err != nil {
			return resolvedRange{}, "timezone", fmt.Errorf("stored timezone is invalid")
		}
		return resolvedRange{dateOnly(start), dateOnly(end), prefs.timezone}, "", nil
	}
	if input.shortcut == nil {
		return resolvedRange{}, "dateRangeEnum", fmt.Errorf("provide dateRangeEnum or startDate and endDate")
	}
	zone := prefs.timezone
	if input.timezone != nil {
		zone = *input.timezone
	}
	location, err := time.LoadLocation(zone)
	if err != nil || zone == "Local" {
		return resolvedRange{}, "tz", fmt.Errorf("must be a valid IANA timezone")
	}
	today := dateOnly(now.In(location))
	start, end := today, today
	switch *input.shortcut {
	case generated.ThisWeek, generated.LastWeek:
		first := time.Monday
		if prefs.firstWeekday == "sunday" {
			first = time.Sunday
		}
		delta := (7 + int(today.Weekday()) - int(first)) % 7
		start = today.AddDate(0, 0, -delta)
		if *input.shortcut == generated.LastWeek {
			start = start.AddDate(0, 0, -7)
		}
		end = start.AddDate(0, 0, 6)
	case generated.Last7Days:
		start = today.AddDate(0, 0, -6)
	case generated.Last30Days:
		start = today.AddDate(0, 0, -29)
	case generated.ThisMonth:
		start = time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
		end = start.AddDate(0, 1, -1)
	case generated.LastMonth:
		end = time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
		start = time.Date(end.Year(), end.Month(), 1, 0, 0, 0, 0, time.UTC)
	case generated.ThisYear:
		start = time.Date(today.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		end = time.Date(today.Year(), 12, 31, 0, 0, 0, 0, time.UTC)
	case generated.LastYear:
		start = time.Date(today.Year()-1, 1, 1, 0, 0, 0, 0, time.UTC)
		end = time.Date(today.Year()-1, 12, 31, 0, 0, 0, 0, time.UTC)
	default:
		return resolvedRange{}, "dateRangeEnum", fmt.Errorf("is not supported")
	}
	return resolvedRange{start, end, zone}, "", nil
}

func dateOnly(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func rangeResponse(value resolvedRange) generated.ResolvedDateRange {
	return generated.ResolvedDateRange{StartDate: openapi_types.Date{Time: value.start}, EndDate: openapi_types.Date{Time: value.end}, Timezone: value.timezone}
}

func parseWorkoutSort(raw *[]string) ([]workoutSort, error) {
	if raw == nil || len(*raw) == 0 {
		return []workoutSort{{"date", "desc"}}, nil
	}
	if len(*raw) > len(workoutSortExpressions) {
		return nil, fmt.Errorf("must not contain more than %d fields", len(workoutSortExpressions))
	}
	seen := make(map[string]bool, len(*raw))
	result := make([]workoutSort, 0, len(*raw))
	for _, item := range *raw {
		parts := strings.Split(item, ":")
		if len(parts) != 2 || workoutSortExpressions[parts[0]] == "" || (parts[1] != "asc" && parts[1] != "desc") {
			return nil, fmt.Errorf("must contain only allowlisted field:direction values")
		}
		if seen[parts[0]] {
			return nil, fmt.Errorf("contains duplicate field %q", parts[0])
		}
		seen[parts[0]] = true
		result = append(result, workoutSort{parts[0], parts[1]})
	}
	return result, nil
}

const workoutSelect = `SELECT w.id,w.source_id,wt.id,wt.type_key,wt.provider_label,w.started_at,w.ended_at,
 w.provider_duration::text,w.local_start_date,w.timezone_name,w.start_offset_minutes,w.end_offset_minutes,
 w.is_indoor,w.location,metrics.distance_value::text,metrics.distance_unit,
	 CASE WHEN metrics.speed_value > 0 THEN trim_scale(60 / metrics.speed_value)::text END,
 CASE WHEN metrics.speed_value > 0 THEN 'min/km' END,metrics.energy_value::text,metrics.energy_unit,
 metrics.heart_rate_value::text,metrics.heart_rate_unit,metrics.elevation_value::text,metrics.elevation_unit,
 (SELECT count(*)::integer FROM app.workout_route_points point WHERE point.workout_id=w.id) AS route_count
 FROM app.workouts w JOIN app.workout_types wt ON wt.id=w.workout_type_id
 LEFT JOIN LATERAL (SELECT
  max(value) FILTER (WHERE metric='distance' AND unit='km') AS distance_value,max(unit) FILTER (WHERE metric='distance' AND unit='km') AS distance_unit,
  max(value) FILTER (WHERE metric='speed_average' AND unit='km/hr') AS speed_value,max(unit) FILTER (WHERE metric='speed_average' AND unit='km/hr') AS speed_unit,
  COALESCE(max(value) FILTER (WHERE metric='active_energy_burned' AND unit='kcal'),max(value) FILTER (WHERE metric='total_energy' AND unit='kcal')) AS energy_value,
  COALESCE(max(unit) FILTER (WHERE metric='active_energy_burned' AND unit='kcal'),max(unit) FILTER (WHERE metric='total_energy' AND unit='kcal')) AS energy_unit,
  max(value) FILTER (WHERE metric='heart_rate_average' AND unit='count/min') AS heart_rate_value,max(unit) FILTER (WHERE metric='heart_rate_average' AND unit='count/min') AS heart_rate_unit,
  max(value) FILTER (WHERE metric='elevation_up' AND unit='m') AS elevation_value,max(unit) FILTER (WHERE metric='elevation_up' AND unit='m') AS elevation_unit
 FROM app.workout_aggregates aggregate WHERE aggregate.workout_id=w.id) metrics ON true`

func queryWorkouts(ctx context.Context, tx pgx.Tx, dateRange resolvedRange, page, pageSize int, sorts []workoutSort) ([]generated.Workout, error) {
	order := make([]string, 0, len(sorts)+1)
	for _, sort := range sorts {
		order = append(order, workoutSortExpressions[sort.field]+" "+sort.direction+" NULLS LAST")
	}
	order = append(order, "w.id ASC")
	query := workoutSelect + ` WHERE w.local_start_date BETWEEN $1 AND $2 ORDER BY ` + strings.Join(order, ",") + ` LIMIT $3 OFFSET $4`
	rows, err := tx.Query(ctx, query, dateRange.start, dateRange.end, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []generated.Workout{}
	for rows.Next() {
		item, err := scanWorkout(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanWorkout(row interface{ Scan(...any) error }) (generated.Workout, error) {
	var item generated.Workout
	var id, sourceID, typeID uuid.UUID
	var localDate *time.Time
	var timezone *string
	var startOffset, endOffset *int
	var indoor *bool
	var location *string
	var distanceValue, distanceUnit, paceValue, paceUnit, energyValue, energyUnit *string
	var heartValue, heartUnit, elevationValue, elevationUnit *string
	err := row.Scan(&id, &sourceID, &typeID, &item.Type.Key, &item.Type.DisplayName, &item.StartedAt, &item.EndedAt,
		&item.Duration, &localDate, &timezone, &startOffset, &endOffset, &indoor, &location,
		&distanceValue, &distanceUnit, &paceValue, &paceUnit, &energyValue, &energyUnit,
		&heartValue, &heartUnit, &elevationValue, &elevationUnit, &item.RoutePointCount)
	if err != nil {
		return item, err
	}
	item.Id, item.SourceId, item.Type.Id = compactUUID(id), compactUUID(sourceID), compactUUID(typeID)
	setNullable(&item.LocalStartDate, mapDate(localDate))
	setNullable(&item.Timezone, timezone)
	setNullable(&item.OriginalStartOffsetMinutes, startOffset)
	setNullable(&item.OriginalEndOffsetMinutes, endOffset)
	setNullable(&item.Indoor, indoor)
	setNullable(&item.Location, location)
	setMetric(&item.Distance, distanceValue, distanceUnit)
	setMetric(&item.Pace, paceValue, paceUnit)
	setMetric(&item.Calories, energyValue, energyUnit)
	setMetric(&item.HeartRate, heartValue, heartUnit)
	setMetric(&item.Elevation, elevationValue, elevationUnit)
	item.RouteAvailable = item.RoutePointCount > 0
	setNullable(&item.DisplayTimezone, displayTimezone(timezone, startOffset))
	return item, nil
}

func querySummary(ctx context.Context, tx pgx.Tx, dateRange resolvedRange) ([]generated.WorkoutTypeSummary, generated.SummaryTotals, error) {
	rows, err := tx.Query(ctx, `SELECT wt.id,wt.type_key,wt.provider_label,count(*)::bigint,trim_scale(sum(w.provider_duration))::text,
		 trim_scale(sum(metrics.distance))::text,trim_scale(sum(metrics.energy))::text
	 FROM app.workouts w JOIN app.workout_types wt ON wt.id=w.workout_type_id
	 LEFT JOIN LATERAL (SELECT max(value) FILTER (WHERE metric='distance' AND unit='km') AS distance,
	  COALESCE(max(value) FILTER (WHERE metric='active_energy_burned' AND unit='kcal'),max(value) FILTER (WHERE metric='total_energy' AND unit='kcal')) AS energy
	  FROM app.workout_aggregates a WHERE a.workout_id=w.id) metrics ON true
	 WHERE w.local_start_date BETWEEN $1 AND $2 GROUP BY wt.id,wt.type_key,wt.provider_label
 ORDER BY wt.provider_label COLLATE "C",wt.type_key,wt.id`, dateRange.start, dateRange.end)
	if err != nil {
		return nil, generated.SummaryTotals{}, err
	}
	defer rows.Close()
	items := []generated.WorkoutTypeSummary{}
	var totalCount int64
	for rows.Next() {
		var id uuid.UUID
		var item generated.WorkoutTypeSummary
		var duration string
		var distance, energy *string
		if err := rows.Scan(&id, &item.Type.Key, &item.Type.DisplayName, &item.Totals.Count, &duration, &distance, &energy); err != nil {
			return nil, generated.SummaryTotals{}, err
		}
		item.Type.Id = compactUUID(id)
		item.Totals.Duration = duration
		setMetric(&item.Totals.Distance, distance, stringPointer("km"))
		setMetric(&item.Totals.Energy, energy, stringPointer("kcal"))
		totalCount += item.Totals.Count
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, generated.SummaryTotals{}, err
	}
	// PostgreSQL computes the exact overall sums, avoiding decimal arithmetic in Go.
	var duration, distance, energy *string
	err = tx.QueryRow(ctx, `SELECT trim_scale(sum(provider_duration))::text,
		 (SELECT trim_scale(sum(value))::text FROM app.workout_aggregates a JOIN app.workouts x ON x.id=a.workout_id WHERE x.local_start_date BETWEEN $1 AND $2 AND a.metric='distance' AND a.unit='km'),
		 (SELECT trim_scale(sum(energy))::text FROM (SELECT COALESCE(max(value) FILTER (WHERE metric='active_energy_burned' AND unit='kcal'),max(value) FILTER (WHERE metric='total_energy' AND unit='kcal')) AS energy
	  FROM app.workout_aggregates a JOIN app.workouts x ON x.id=a.workout_id WHERE x.local_start_date BETWEEN $1 AND $2 GROUP BY x.id) energies)
	 FROM app.workouts WHERE local_start_date BETWEEN $1 AND $2`, dateRange.start, dateRange.end).Scan(&duration, &distance, &energy)
	if err != nil {
		return nil, generated.SummaryTotals{}, err
	}
	totals := generated.SummaryTotals{Count: totalCount}
	if totalCount == 0 {
		duration, distance, energy = stringPointer("0"), stringPointer("0"), stringPointer("0")
	}
	totals.Duration = *duration
	setMetric(&totals.Distance, distance, stringPointer("km"))
	setMetric(&totals.Energy, energy, stringPointer("kcal"))
	return items, totals, nil
}

func setMetric(target *nullable.Nullable[generated.ExactMetric], value, unit *string) {
	if value == nil || unit == nil {
		target.SetNull()
		return
	}
	target.Set(generated.ExactMetric{Value: *value, Unit: *unit})
}

func setNullable[T any](target *nullable.Nullable[T], value *T) {
	if value == nil {
		target.SetNull()
		return
	}
	target.Set(*value)
}

func mapDate(value *time.Time) *openapi_types.Date {
	if value == nil {
		return nil
	}
	return &openapi_types.Date{Time: *value}
}

func displayTimezone(zone *string, offset *int) *string {
	if zone != nil {
		return zone
	}
	if offset == nil {
		return nil
	}
	sign, minutes := "+", *offset
	if minutes < 0 {
		sign, minutes = "-", -minutes
	}
	return stringPointer(fmt.Sprintf("UTC%s%02d:%02d", sign, minutes/60, minutes%60))
}

func stringPointer(value string) *string { return &value }

func writeWorkoutFieldError(w http.ResponseWriter, r *http.Request, field, message string) {
	writeValidationProblem(w, r, http.StatusBadRequest, "workout query is invalid", generated.ValidationError{Field: field, Code: "invalid", Message: &message})
}

func writeWorkoutRangeError(w http.ResponseWriter, r *http.Request, field, message string) {
	writeWorkoutFieldError(w, r, field, message)
}

func writeWorkoutUnavailable(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "workout service is temporarily unavailable")
}
