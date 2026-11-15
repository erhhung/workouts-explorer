package api

import (
	"strings"
	"testing"
	"time"
)

func TestWorkoutFilterValidation(t *testing.T) {
	for _, raw := range []string{
		`null`, `[]`, `{}`, `{"weekdays":[]}`, `{"typeIds":[]}`,
		`{"weekdays":[0,6],"duration":{"operator":"gte","value":"900.125"}}`,
	} {
		_, err := parseWorkoutFilters(&raw)
		valid := raw != "null" && raw != "[]"
		if (err == nil) != valid {
			t.Errorf("parse %s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		``, `{} {}`, `{"other":1}`, `{"weekdays":[7]}`, `{"weekdays":[1,1]}`,
		`{"weekdays":null}`, `{"weekdays":[null]}`, `{"typeIds":["foreign-or-invalid"]}`, `{"distance":null}`,
		`{"duration":{"operator":"gt; SELECT 1","value":"1"}}`,
		`{"duration":{"operator":"gt","value":"-1"}}`,
		`{"duration":{"operator":"gt","value":"NaN"}}`,
		`{"duration":{"operator":"gt","value":"1e10"}}`,
		`{"duration":{"operator":"gt","value":"1","other":1}}`,
		`{"distance":{"operator":"gt","value":"1","unit":"s/mi"}}`,
		`{"pace":{"operator":"gt","value":"1","unit":"mi"}}`,
		`{"duration":{"value":"1"}}`, `{"duration":{"operator":"gt"}}`,
		`{"duration":{"operator":"gt","value":"` + strings.Repeat("1", 65) + `"}}`,
		strings.Repeat(" ", 16384) + `{}`,
	} {
		if _, err := parseWorkoutFilters(&raw); err == nil {
			t.Errorf("accepted invalid filter %q", raw)
		}
	}
}

func TestWorkoutFilterPredicates(t *testing.T) {
	raw := `{"weekdays":[],"typeIds":[],"distance":{"operator":"gte","value":"1.609344"},"pace":{"operator":"lt","value":"10"}}`
	filters, err := parseWorkoutFilters(&raw)
	if err != nil {
		t.Fatal(err)
	}
	rangeValue := resolvedRange{start: time.Now(), end: time.Now()}
	where, args := workoutWhere(rangeValue, filters)
	if len(args) != 6 || !strings.Contains(where, "BETWEEN $1 AND $2 AND extract(dow") ||
		!strings.Contains(where, "ANY($3::integer[])") || !strings.Contains(where, "ANY($4::uuid[])") ||
		!strings.Contains(where, ">= $5::numeric") || !strings.Contains(where, "< $6::numeric") ||
		strings.Contains(where, "1.609344") {
		t.Fatalf("unsafe or incorrectly numbered predicates: %s %#v", where, args)
	}
	rangeValue.allTime = true
	where, args = workoutWhere(rangeValue, filters)
	if len(args) != 4 || strings.Contains(where, "BETWEEN") || !strings.Contains(where, "ANY($1::integer[])") {
		t.Fatalf("all-time predicates: %s %#v", where, args)
	}
}
