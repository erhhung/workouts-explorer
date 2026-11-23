package osm

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

func TestUpdateTaskInventoryMatchesDocumentedOrder(t *testing.T) {
	body, err := os.ReadFile("../../docs/osm-resumable-rebuild.md")
	if err != nil {
		t.Fatal(err)
	}
	matches := regexp.MustCompile("(?m)^\\| ([0-9]+) \\| `([^`]+)` ").FindAllSubmatch(body, -1)
	if len(matches) != 58 || len(UpdateTasks) != 58 {
		t.Fatalf("document tasks=%d implementation=%d", len(matches), len(UpdateTasks))
	}
	for i, match := range matches {
		order, _ := strconv.Atoi(string(match[1]))
		if UpdateTasks[i].Order != order || UpdateTasks[i].Name != string(match[2]) {
			t.Fatalf("task %d differs from inventory", i+1)
		}
	}
}
