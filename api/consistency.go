package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/jackc/pgx/v5"
)

const maximumConsistencyBuckets = 20000

var errConsistencyRange = errors.New("choose a shorter date range or monthly grouping")

func (s *Server) GetConsistencyReport(w http.ResponseWriter, r *http.Request, params generated.GetConsistencyReportParams) {
	session, ok := s.requireSession(w, r, "user")
	if !ok {
		return
	}
	group := generated.ConsistencyGroupingWeek
	if params.GroupBy != nil {
		group = *params.GroupBy
	}
	if !group.Valid() || params.FirstWeekday != nil && !params.FirstWeekday.Valid() {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "grouping or first weekday is invalid")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	tx, err := s.statsTransaction(ctx, session, true)
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
	if params.FirstWeekday != nil {
		prefs.firstWeekday = string(*params.FirstWeekday)
	}
	now := time.Now()
	resolved, field, err := resolveAccountRange(ctx, tx, rangeInput{params.StartDate, params.EndDate, params.DateRangeEnum, (*string)(params.Tz)}, prefs, now)
	if err != nil {
		if field != "" {
			writeWorkoutRangeError(w, r, field, err.Error())
		} else {
			writeStatsError(w, r, err)
		}
		return
	}
	result, err := queryConsistency(ctx, tx, resolved, group, prefs.firstWeekday, now)
	if errors.Is(err, errConsistencyRange) {
		writeValidationProblem(w, r, http.StatusUnprocessableEntity, err.Error(), generated.ValidationError{Field: "dateRange", Code: "limit"})
		return
	}
	if err != nil || tx.Commit(ctx) != nil {
		writeStatsError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, result)
}

func consistencyBucketStart(date time.Time, group generated.ConsistencyGrouping, firstWeekday string) time.Time {
	date = dateOnly(date)
	if group == generated.ConsistencyGroupingMonth {
		return time.Date(date.Year(), date.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	first := time.Monday
	if firstWeekday == "sunday" {
		first = time.Sunday
	}
	return date.AddDate(0, 0, -(7+int(date.Weekday())-int(first))%7)
}

func consistencyBucketBounds(dateRange resolvedRange, group generated.ConsistencyGrouping, firstWeekday string) (time.Time, time.Time, error) {
	start := consistencyBucketStart(dateRange.start, group, firstWeekday)
	end := consistencyBucketStart(dateRange.end, group, firstWeekday)
	if start.Year() < 1 || end.Year() > 9999 || end.Before(start) {
		return start, end, errConsistencyRange
	}
	count := (end.Year()-start.Year())*12 + int(end.Month()-start.Month()) + 1
	if group == generated.ConsistencyGroupingWeek {
		count = int((end.Unix()-start.Unix())/(7*86400)) + 1
	}
	if count > maximumConsistencyBuckets {
		return start, end, errConsistencyRange
	}
	return start, end, nil
}

func consistencyVisibleRange(dateRange resolvedRange, now time.Time) (resolvedRange, bool, error) {
	location, err := time.LoadLocation(dateRange.timezone)
	if err != nil {
		return dateRange, false, err
	}
	today := dateOnly(now.In(location))
	if dateRange.end.After(today) {
		dateRange.end = today
	}
	return dateRange, !dateRange.end.Before(dateRange.start), nil
}

func queryConsistency(ctx context.Context, tx pgx.Tx, dateRange resolvedRange, group generated.ConsistencyGrouping, firstWeekday string, clock ...time.Time) (generated.ConsistencyReport, error) {
	result := generated.ConsistencyReport{Range: rangeResponse(dateRange), GroupBy: group,
		FirstWeekday: generated.ConsistencyReportFirstWeekday(firstWeekday), Buckets: []generated.ConsistencyBucket{}}
	if dateRange.allTime {
		if err := tx.QueryRow(ctx, `SELECT count(*)::bigint FROM app.workouts WHERE local_start_date IS NULL AND deletion_requested_at IS NULL`).Scan(&result.UndatedWorkoutCount); err != nil {
			return result, err
		}
	}
	now := time.Now()
	if len(clock) > 0 {
		now = clock[0]
	}
	visible, periods, err := consistencyVisibleRange(dateRange, now)
	if err != nil || !periods {
		return result, err
	}
	dateRange = visible
	start, end, err := consistencyBucketBounds(dateRange, group, firstWeekday)
	if err != nil {
		return result, err
	}
	first := 1
	if firstWeekday == "sunday" {
		first = 0
	}
	rows, err := tx.Query(ctx, `WITH workouts AS MATERIALIZED (
		SELECT CASE WHEN $7::text='week' THEN w.local_start_date-((extract(dow FROM w.local_start_date)::integer-$1::integer+7)%7)
			ELSE date_trunc('month',w.local_start_date::timestamp)::date END bucket_start,
			w.local_start_date,w.provider_duration,metrics.distance,metrics.calories
		FROM app.workouts w
		LEFT JOIN LATERAL (
			SELECT max(value) FILTER (WHERE metric='distance' AND unit='km') distance,
				max(value) FILTER (WHERE metric='total_energy' AND unit='kcal') calories
			FROM app.workout_aggregates a WHERE a.workout_id=w.id
		) metrics ON true
		WHERE w.local_start_date IS NOT NULL AND w.deletion_requested_at IS NULL
			AND ($6::boolean OR w.local_start_date BETWEEN $4::date AND $5::date)
			AND w.local_start_date <= $8::date
	), aggregated AS (
		SELECT bucket_start,count(*)::bigint workout_count,count(DISTINCT local_start_date)::integer active_days,
			sum(provider_duration) duration,sum(distance) distance,sum(calories) calories,
			max(provider_duration) longest_duration,max(distance) longest_distance,max(calories) highest_calories
		FROM workouts GROUP BY bucket_start
	), calendar AS (
		SELECT bucket::date bucket_start FROM generate_series($2::date::timestamp,$3::date::timestamp,
			CASE WHEN $7='week' THEN interval '1 week' ELSE interval '1 month' END) bucket
	)
	SELECT c.bucket_start,coalesce(a.workout_count,0),coalesce(a.active_days,0),coalesce(trim_scale(a.duration)::text,'0'),
		trim_scale(a.distance)::text,trim_scale(a.calories)::text,trim_scale(a.longest_duration)::text,
		trim_scale(a.longest_distance)::text,trim_scale(a.highest_calories)::text
	FROM calendar c LEFT JOIN aggregated a USING(bucket_start) ORDER BY c.bucket_start`,
		first, start, end, dateRange.start, dateRange.end, dateRange.allTime, string(group), dateRange.end)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var bucket generated.ConsistencyBucket
		var distance, calories, longestDuration, longestDistance, highestCalories *string
		if err := rows.Scan(&bucket.BucketStart.Time, &bucket.WorkoutCount, &bucket.ActiveDays, &bucket.Duration,
			&distance, &calories, &longestDuration, &longestDistance, &highestCalories); err != nil {
			rows.Close()
			return result, err
		}
		if bucket.WorkoutCount == 0 {
			distance, calories = stringPointer("0"), stringPointer("0")
		}
		setMetric(&bucket.Distance, distance, stringPointer("km"))
		setMetric(&bucket.Calories, calories, stringPointer("kcal"))
		setNullable(&bucket.LongestDuration, longestDuration)
		setMetric(&bucket.LongestDistance, longestDistance, stringPointer("km"))
		setMetric(&bucket.HighestCalories, highestCalories, stringPointer("kcal"))
		result.Buckets = append(result.Buckets, bucket)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	body, err := json.Marshal(result)
	if err == nil && len(body) > maximumStatsResponseBytes {
		err = errConsistencyRange
	}
	return result, err
}
