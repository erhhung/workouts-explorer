package osm

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCompletedCheckpointTasks(t *testing.T) {
	for _, test := range []struct {
		stage, state, cursor string
		want                 []int
	}{
		{"attribute-slivers", "completed", `{"phase":"done"}`, []int{27, 28}},
		{"attribute-slivers", "running", `{"phase":"apply"}`, []int{27}},
		{"identity-edges", "running", `{"orientation":4}`, []int{30, 31}},
		{"identity-propagate", "running", `{"phase":"compress"}`, nil},
		{"identity-propagate", "running", `{"phase":"hook"}`, nil},
		{"identity-propagate", "running", `{"phase":"assign"}`, []int{43, 44}},
		{"validate", "completed", `{"phase":"done"}`, nil},
	} {
		t.Run(test.stage+test.state+test.cursor, func(t *testing.T) {
			got := CompletedCheckpointTasks(test.stage, test.state, json.RawMessage(test.cursor))
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("completed=%v want%v", got, test.want)
			}
		})
	}
}
