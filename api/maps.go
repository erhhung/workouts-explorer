package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

const (
	mapSelectionLifetime = 30 * time.Minute
	maximumTileBytes     = 5 << 20
)

func (s *Server) CreateMapSelection(w http.ResponseWriter, r *http.Request, params generated.CreateMapSelectionParams) {
	session, ok := s.requireSession(w, r, "user")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	var input generated.MapSelectionCreate
	if !decodeJSON(w, r, &input) {
		return
	}
	accountID := *session.accountID
	tx, err := s.sessionAccountTransactionWithOptions(r.Context(), session, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		writeMapUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	prefs, err := queryReadPreferences(r.Context(), tx, accountID)
	if err != nil {
		writeMapUnavailable(w, r)
		return
	}
	resolved, field, err := resolveRange(rangeInput{input.StartDate, input.EndDate, input.DateRangeEnum, input.Tz}, prefs, time.Now())
	if err != nil {
		writeWorkoutRangeError(w, r, field, err.Error())
		return
	}
	workoutIDs, valid := normalizedMapWorkoutIDs(input.WorkoutIds)
	if !valid {
		writeFieldError(w, r, "workoutIds", "unique", "workoutIds must identify unique workouts")
		return
	}
	var generation int64
	if err := tx.QueryRow(r.Context(), `SELECT app.lock_account_data_generation()`).Scan(&generation); err != nil {
		writeMapUnavailable(w, r)
		return
	}
	selectionID, err := uuid.NewV7()
	if err != nil {
		writeMapUnavailable(w, r)
		return
	}
	expiresAt := time.Now().UTC().Add(mapSelectionLifetime)
	if session.expiresAt.Before(expiresAt) {
		expiresAt = session.expiresAt
	}
	if _, err := tx.Exec(r.Context(), `INSERT INTO app.map_selections(id,account_id,session_id,generation,expires_at) VALUES($1,$2,$3,$4,$5)`, selectionID, accountID, session.sessionID, generation, expiresAt); err != nil {
		writeMapUnavailable(w, r)
		return
	}
	inserted, err := insertMapSelectionWorkouts(r.Context(), tx, accountID, selectionID, resolved, workoutIDs, input.WorkoutIds != nil)
	if err != nil {
		writeMapUnavailable(w, r)
		return
	}
	if input.WorkoutIds != nil && inserted != int64(len(workoutIDs)) {
		writeProblem(w, r, http.StatusNotFound, "Not Found", "one or more routed workouts are unavailable")
		return
	}
	if input.WorkoutIds == nil && inserted > 5000 {
		writeFieldError(w, r, "dateRangeEnum", "limit", "the resolved period contains more than 5000 routed workouts")
		return
	}
	response, err := readMapSelection(r.Context(), tx, accountID, selectionID, generation, expiresAt, resolved)
	if err != nil || tx.Commit(r.Context()) != nil {
		writeMapUnavailable(w, r)
		return
	}
	w.Header().Set("Location", "/api/map-selections/"+response.Id)
	writeJSON(w, http.StatusCreated, response)
}

func normalizedMapWorkoutIDs(values *[]generated.UUIDInput) ([]uuid.UUID, bool) {
	if values == nil {
		return nil, true
	}
	result := make([]uuid.UUID, 0, len(*values))
	seen := make(map[uuid.UUID]struct{}, len(*values))
	for _, value := range *values {
		id, ok := parseCompactUUID(string(value))
		if !ok {
			return nil, false
		}
		if _, exists := seen[id]; exists {
			return nil, false
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, true
}

func insertMapSelectionWorkouts(ctx context.Context, tx pgx.Tx, accountID, selectionID uuid.UUID, resolved resolvedRange, workoutIDs []uuid.UUID, explicit bool) (int64, error) {
	filter := ""
	limit := " LIMIT 5001"
	arguments := []any{accountID, selectionID, resolved.start, resolved.end}
	if explicit {
		filter = " AND workout.id=ANY($5::uuid[])"
		limit = ""
		arguments = append(arguments, workoutIDs)
	}
	command, err := tx.Exec(ctx, `
		INSERT INTO app.map_selection_workouts(account_id,selection_id,workout_id,sort_order)
		SELECT $1,$2,workout.id,(row_number() OVER (ORDER BY workout.started_at,workout.id)-1)::integer
		  FROM app.workouts workout
		  JOIN app.workout_routes route ON route.workout_id=workout.id AND route.account_id=workout.account_id
		 WHERE workout.account_id=$1 AND workout.local_start_date BETWEEN $3 AND $4
		   AND workout.deletion_requested_at IS NULL AND route.route IS NOT NULL`+filter+limit, arguments...)
	if err != nil {
		return 0, err
	}
	return command.RowsAffected(), nil
}

func readMapSelection(ctx context.Context, tx pgx.Tx, accountID, selectionID uuid.UUID, generation int64, expiresAt time.Time, resolved resolvedRange) (generated.MapSelection, error) {
	result := generated.MapSelection{
		Id: compactUUID(selectionID), DataGeneration: generation, ExpiresAt: expiresAt, Range: rangeResponse(resolved),
		RouteTileUrl: fmt.Sprintf("/api/map-selections/%s/route-tiles/%d/{z}/{x}/{y}.pbf", compactUUID(selectionID), generation),
		Workouts:     make([]generated.MapSelectionWorkout, 0),
	}
	rows, err := tx.Query(ctx, `
		SELECT workout.id,workout_type.id,workout_type.type_key,workout_type.provider_label,
		       workout.started_at,workout.ended_at,workout.provider_duration::text,workout.local_start_date,
		       route.minimum_longitude,route.minimum_latitude,route.maximum_longitude,route.maximum_latitude,
		       metrics.distance_value::text,metrics.distance_unit,
		       CASE WHEN metrics.speed_value>0 THEN trim_scale(60/metrics.speed_value)::text END,
		       CASE WHEN metrics.speed_value>0 THEN 'min/km' END,
		       metrics.energy_value::text,metrics.energy_unit,metrics.heart_rate_value::text,metrics.heart_rate_unit,
		       metrics.elevation_value::text,metrics.elevation_unit
		  FROM app.map_selection_workouts selected
		  JOIN app.workouts workout ON workout.id=selected.workout_id AND workout.account_id=selected.account_id
		  JOIN app.workout_types workout_type ON workout_type.id=workout.workout_type_id AND workout_type.account_id=workout.account_id
		  JOIN app.workout_routes route ON route.workout_id=workout.id AND route.account_id=workout.account_id
		  LEFT JOIN LATERAL (SELECT
		    max(value) FILTER (WHERE metric='distance' AND unit='km') AS distance_value,max(unit) FILTER (WHERE metric='distance' AND unit='km') AS distance_unit,
		    max(value) FILTER (WHERE metric='speed_average' AND unit='km/hr') AS speed_value,
		    COALESCE(max(value) FILTER (WHERE metric='active_energy_burned' AND unit='kcal'),max(value) FILTER (WHERE metric='total_energy' AND unit='kcal')) AS energy_value,
		    COALESCE(max(unit) FILTER (WHERE metric='active_energy_burned' AND unit='kcal'),max(unit) FILTER (WHERE metric='total_energy' AND unit='kcal')) AS energy_unit,
		    max(value) FILTER (WHERE metric='heart_rate_average' AND unit='count/min') AS heart_rate_value,max(unit) FILTER (WHERE metric='heart_rate_average' AND unit='count/min') AS heart_rate_unit,
		    max(value) FILTER (WHERE metric='elevation_up' AND unit='m') AS elevation_value,max(unit) FILTER (WHERE metric='elevation_up' AND unit='m') AS elevation_unit
		    FROM app.workout_aggregates aggregate WHERE aggregate.workout_id=workout.id AND aggregate.account_id=workout.account_id) metrics ON true
		 WHERE selected.account_id=$1 AND selected.selection_id=$2
		 ORDER BY selected.sort_order`, accountID, selectionID)
	if err != nil {
		return generated.MapSelection{}, err
	}
	defer rows.Close()
	var bounds *generated.RouteBounds
	for rows.Next() {
		var workoutID, typeID uuid.UUID
		var typeKey, typeName string
		var startedAt, endedAt time.Time
		var duration string
		var localStartDate *time.Time
		var distanceValue, distanceUnit, paceValue, paceUnit, caloriesValue, caloriesUnit *string
		var heartRateValue, heartRateUnit, elevationValue, elevationUnit *string
		var minimumLongitude, minimumLatitude, maximumLongitude, maximumLatitude float64
		if err := rows.Scan(&workoutID, &typeID, &typeKey, &typeName, &startedAt, &endedAt, &duration, &localStartDate,
			&minimumLongitude, &minimumLatitude, &maximumLongitude, &maximumLatitude,
			&distanceValue, &distanceUnit, &paceValue, &paceUnit, &caloriesValue, &caloriesUnit,
			&heartRateValue, &heartRateUnit, &elevationValue, &elevationUnit); err != nil {
			return generated.MapSelection{}, err
		}
		item := generated.MapSelectionWorkout{
			Id: compactUUID(workoutID), StartedAt: startedAt, EndedAt: endedAt, Duration: duration, PartialRoute: false,
			Type:   generated.MapSelectionWorkoutType{Id: compactUUID(typeID), Key: typeKey, Name: typeName},
			Bounds: generated.RouteBounds{MinimumLongitude: minimumLongitude, MinimumLatitude: minimumLatitude, MaximumLongitude: maximumLongitude, MaximumLatitude: maximumLatitude},
		}
		setMetric(&item.Distance, distanceValue, distanceUnit)
		setMetric(&item.Pace, paceValue, paceUnit)
		setMetric(&item.Calories, caloriesValue, caloriesUnit)
		setMetric(&item.HeartRate, heartRateValue, heartRateUnit)
		setMetric(&item.Elevation, elevationValue, elevationUnit)
		if localStartDate == nil {
			item.LocalStartDate.SetNull()
		} else {
			item.LocalStartDate.Set(openapi_types.Date{Time: *localStartDate})
		}
		result.Workouts = append(result.Workouts, item)
		if bounds == nil {
			bounds = &generated.RouteBounds{MinimumLongitude: minimumLongitude, MinimumLatitude: minimumLatitude, MaximumLongitude: maximumLongitude, MaximumLatitude: maximumLatitude}
		} else {
			bounds.MinimumLongitude = min(bounds.MinimumLongitude, minimumLongitude)
			bounds.MinimumLatitude = min(bounds.MinimumLatitude, minimumLatitude)
			bounds.MaximumLongitude = max(bounds.MaximumLongitude, maximumLongitude)
			bounds.MaximumLatitude = max(bounds.MaximumLatitude, maximumLatitude)
		}
	}
	if err := rows.Err(); err != nil {
		return generated.MapSelection{}, err
	}
	if bounds == nil {
		result.Bounds.SetNull()
	} else {
		result.Bounds.Set(*bounds)
	}
	return result, nil
}

func (s *Server) DeleteMapSelection(w http.ResponseWriter, r *http.Request, mapSelectionID generated.MapSelectionID, params generated.DeleteMapSelectionParams) {
	session, ok := s.requireSession(w, r, "user")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	id, ok := parseCompactUUID(string(mapSelectionID))
	if !ok {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "map selection ID is invalid")
		return
	}
	tx, err := s.sessionAccountTransactionWithOptions(r.Context(), session, pgx.TxOptions{})
	if err != nil {
		writeMapUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `DELETE FROM app.map_selections WHERE account_id=$1 AND id=$2 AND session_id=$3`, *session.accountID, id, session.sessionID); err != nil || tx.Commit(r.Context()) != nil {
		writeMapUnavailable(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) GetMapSelectionRouteTile(w http.ResponseWriter, r *http.Request, mapSelectionID generated.MapSelectionID, generation int64, z, x, y int) {
	session, ok := s.requireSession(w, r, "user")
	if !ok {
		return
	}
	id, valid := parseCompactUUID(string(mapSelectionID))
	if !valid || z < 0 || z > 22 || x < 0 || y < 0 {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "tile coordinates are invalid")
		return
	}
	coordinateLimit := int64(1) << z
	if int64(x) >= coordinateLimit || int64(y) >= coordinateLimit {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "tile coordinates are invalid")
		return
	}
	tx, err := s.sessionAccountTransactionWithOptions(r.Context(), session, pgx.TxOptions{})
	if err != nil {
		writeMapUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	var exists bool
	err = tx.QueryRow(r.Context(), `
		SELECT EXISTS(
			SELECT 1 FROM app.map_selections selection
			JOIN app.account_data_generations current_generation ON current_generation.account_id=selection.account_id
			WHERE selection.account_id=$1 AND selection.id=$2 AND selection.session_id=$3
			  AND selection.generation=$4 AND current_generation.generation=$4
			  AND selection.expires_at>transaction_timestamp()
		)`, *session.accountID, id, session.sessionID, generation).Scan(&exists)
	if err != nil || tx.Commit(r.Context()) != nil {
		writeMapUnavailable(w, r)
		return
	}
	if !exists {
		writeProblem(w, r, http.StatusNotFound, "Not Found", "map selection is unavailable")
		return
	}
	upstream, err := mapTileUpstreamURL(s.config.PGTileServURL, id, *session.accountID, session.sessionID, generation, z, x, y)
	if err != nil {
		writeMapUnavailable(w, r)
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream, nil)
	if err != nil {
		writeMapUnavailable(w, r)
		return
	}
	client := s.tileClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(request)
	if err != nil {
		writeProblem(w, r, http.StatusBadGateway, "Bad Gateway", "private map tiles are temporarily unavailable")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !validMVTContentType(response.Header.Get("Content-Type")) {
		writeProblem(w, r, http.StatusBadGateway, "Bad Gateway", "private map tiles are temporarily unavailable")
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumTileBytes+1))
	if err != nil || len(body) > maximumTileBytes {
		writeProblem(w, r, http.StatusBadGateway, "Bad Gateway", "private map tiles are temporarily unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.mapbox-vector-tile")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Vary", "Cookie, Authorization")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func mapTileUpstreamURL(base string, selectionID, accountID, sessionID uuid.UUID, generation int64, z, x, y int) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return "", fmt.Errorf("invalid tile service URL")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/app.raw_route_mvt/" + strconv.Itoa(z) + "/" + strconv.Itoa(x) + "/" + strconv.Itoa(y) + ".pbf"
	query := url.Values{}
	query.Set("target_account_id", accountID.String())
	query.Set("target_session_id", sessionID.String())
	query.Set("target_selection_id", selectionID.String())
	query.Set("target_generation", strconv.FormatInt(generation, 10))
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func validMVTContentType(value string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	return mediaType == "application/vnd.mapbox-vector-tile" || mediaType == "application/x-protobuf" || mediaType == "application/octet-stream"
}

func writeMapUnavailable(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "map service is temporarily unavailable")
}
