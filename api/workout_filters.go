package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// These wire types implement the JSON query's WorkoutFilters OpenAPI schema.
type workoutNumericFilter struct {
	Operator string `json:"operator"`
	Value    string `json:"value"`
	Unit     string `json:"unit,omitempty"`
}

type workoutFilters struct {
	Weekdays      *[]int                `json:"weekdays,omitempty"`
	TypeIDs       *[]string             `json:"typeIds,omitempty"`
	Duration      *workoutNumericFilter `json:"duration,omitempty"`
	Distance      *workoutNumericFilter `json:"distance,omitempty"`
	Pace          *workoutNumericFilter `json:"pace,omitempty"`
	Calories      *workoutNumericFilter `json:"calories,omitempty"`
	HeartRate     *workoutNumericFilter `json:"heartRate,omitempty"`
	ElevationGain *workoutNumericFilter `json:"elevationGain,omitempty"`
}

var workoutFilterDecimal = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)

var workoutFilterOperators = map[string]string{"gt": ">", "gte": ">=", "lt": "<", "lte": "<="}

func (f workoutFilters) numbers() []struct {
	field string
	value *workoutNumericFilter
} {
	return []struct {
		field string
		value *workoutNumericFilter
	}{{"duration", f.Duration}, {"distance", f.Distance}, {"pace", f.Pace},
		{"calories", f.Calories}, {"heartRate", f.HeartRate}, {"elevationGain", f.ElevationGain}}
}

func (f workoutFilters) active() bool {
	if f.Weekdays != nil || f.TypeIDs != nil {
		return true
	}
	for _, item := range f.numbers() {
		if item.value != nil {
			return true
		}
	}
	return false
}

func parseWorkoutFilters(raw *string) (workoutFilters, error) {
	var result workoutFilters
	if raw == nil {
		return result, nil
	}
	text := strings.TrimSpace(*raw)
	if len(text) < 2 || len(*raw) > 16384 || text[0] != '{' {
		return result, fmt.Errorf("must be a JSON filter object of at most 16384 characters")
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return workoutFilters{}, fmt.Errorf("must contain only valid column filters")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return workoutFilters{}, fmt.Errorf("must contain exactly one JSON object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &fields); err != nil {
		return workoutFilters{}, fmt.Errorf("must be a JSON filter object")
	}
	for _, value := range fields {
		if string(value) == "null" {
			return workoutFilters{}, fmt.Errorf("omit disabled filters instead of using null")
		}
	}
	if rawDays, ok := fields["weekdays"]; ok {
		var days []*int
		if err := json.Unmarshal(rawDays, &days); err != nil {
			return workoutFilters{}, fmt.Errorf("weekdays must contain integers")
		}
		for _, day := range days {
			if day == nil {
				return workoutFilters{}, fmt.Errorf("weekdays must not contain null")
			}
		}
	}
	if result.Weekdays != nil {
		seen := map[int]bool{}
		for _, day := range *result.Weekdays {
			if day < 0 || day > 6 || seen[day] {
				return workoutFilters{}, fmt.Errorf("weekdays must contain unique integers from 0 to 6")
			}
			seen[day] = true
		}
	}
	if result.TypeIDs != nil {
		if len(*result.TypeIDs) > 256 {
			return workoutFilters{}, fmt.Errorf("typeIds must not contain more than 256 IDs")
		}
		seen := map[uuid.UUID]bool{}
		for _, value := range *result.TypeIDs {
			id, ok := parseCompactUUID(value)
			if !ok || seen[id] {
				return workoutFilters{}, fmt.Errorf("typeIds must contain unique valid workout type IDs")
			}
			seen[id] = true
		}
	}
	for _, item := range result.numbers() {
		if item.value == nil {
			continue
		}
		if workoutFilterOperators[item.value.Operator] == "" || len(item.value.Value) > 64 || !workoutFilterDecimal.MatchString(item.value.Value) {
			return workoutFilters{}, fmt.Errorf("%s requires an allowed operator and a nonnegative decimal threshold", item.field)
		}
		if item.value.Unit != "" && (item.field != "pace" || (item.value.Unit != "s/km" && item.value.Unit != "s/mi")) {
			return workoutFilters{}, fmt.Errorf("only pace accepts an explicit s/km or s/mi unit")
		}
	}
	return result, nil
}

func workoutWhere(dateRange resolvedRange, filters workoutFilters) (string, []any) {
	base, arguments := dateRange.filter("w.local_start_date", 1)
	conditions := []string{}
	if base != "" {
		conditions = append(conditions, strings.TrimPrefix(base, " WHERE "))
	}
	parameter := func(value any) string {
		arguments = append(arguments, value)
		return fmt.Sprintf("$%d", len(arguments))
	}
	if filters.Weekdays != nil {
		conditions = append(conditions, "extract(dow FROM w.local_start_date)::integer = ANY("+parameter(*filters.Weekdays)+"::integer[])")
	}
	if filters.TypeIDs != nil {
		ids := make([]uuid.UUID, 0, len(*filters.TypeIDs))
		for _, value := range *filters.TypeIDs {
			id, _ := parseCompactUUID(value)
			ids = append(ids, id)
		}
		conditions = append(conditions, "w.workout_type_id = ANY("+parameter(ids)+"::uuid[])")
	}
	for _, item := range filters.numbers() {
		if item.value != nil {
			if item.field == "pace" && item.value.Unit != "" {
				// Cross-multiply before division so inclusive unit boundaries remain exact.
				factor := "1"
				if item.value.Unit == "s/mi" {
					factor = "1.609344"
				}
				conditions = append(conditions, "metrics.distance_value > 0 AND (w.provider_duration * "+factor+") "+workoutFilterOperators[item.value.Operator]+" ("+parameter(item.value.Value)+"::numeric * metrics.distance_value)")
				continue
			}
			conditions = append(conditions, "("+workoutSortExpressions[item.field]+") "+workoutFilterOperators[item.value.Operator]+" "+parameter(item.value.Value)+"::numeric")
		}
	}
	if len(conditions) == 0 {
		return "", arguments
	}
	return " WHERE " + strings.Join(conditions, " AND "), arguments
}

func queryFilteredWorkoutCount(ctx context.Context, tx pgx.Tx, dateRange resolvedRange, filters workoutFilters) (int64, error) {
	where, arguments := workoutWhere(dateRange, filters)
	var total int64
	err := tx.QueryRow(ctx, "SELECT count(*)::bigint "+workoutFrom+where, arguments...).Scan(&total)
	return total, err
}

func queryWorkoutFilterTypes(ctx context.Context, tx pgx.Tx, dateRange resolvedRange) ([]generated.WorkoutType, error) {
	where, arguments := dateRange.filter("w.local_start_date", 1)
	rows, err := tx.Query(ctx, `SELECT DISTINCT wt.id,wt.type_key,wt.provider_label
		FROM app.workouts w JOIN app.workout_types wt ON wt.id=w.workout_type_id`+where, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	types := []generated.WorkoutType{}
	for rows.Next() {
		var item generated.WorkoutType
		var id uuid.UUID
		if err := rows.Scan(&id, &item.Key, &item.DisplayName); err != nil {
			return nil, err
		}
		item.Id = compactUUID(id)
		item.DisplayName = healthautoexport.CanonicalActivityLabel(item.DisplayName)
		types = append(types, item)
	}
	sort.Slice(types, func(i, j int) bool {
		if types[i].DisplayName != types[j].DisplayName {
			return types[i].DisplayName < types[j].DisplayName
		}
		return types[i].Id < types[j].Id
	})
	return types, rows.Err()
}
