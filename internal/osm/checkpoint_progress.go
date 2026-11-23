package osm

import (
	"encoding/json"
	"strings"
)

// CompletedCheckpointTasks maps committed pipeline progress to logical UI tasks.
// Validation is deliberately replayed on resume, so its old results are excluded.
func CompletedCheckpointTasks(stage, state string, cursor json.RawMessage) []int {
	if stage == "validate" {
		return nil
	}
	var fields struct {
		Phase       string
		Orientation int
	}
	_ = json.Unmarshal(cursor, &fields)
	phase := fields.Phase
	if stage == "identity-edges" {
		switch fields.Orientation {
		case 0, 1:
			phase = "orientation-1"
		case 2:
			phase = "orientation-2"
		case 4:
			phase = "orientation-4"
		default:
			phase = "finalize"
		}
	}
	current := logicalTask(stage, phase)
	if stage == "identity-propagate" && phase != "assign" && state != "completed" {
		return nil
	}
	var result []int
	for _, task := range UpdateTasks {
		if task.Name != stage && !strings.HasPrefix(task.Name, stage+"/") {
			continue
		}
		if state == "completed" || (current.Order > 0 && task.Order < current.Order) {
			result = append(result, task.Order)
		}
	}
	return result
}
