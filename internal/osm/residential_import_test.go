package osm

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestResidentialImporterKeepsLegacyStageInventoryAndFences(t *testing.T) {
	legacy := Generation{ImporterVersion: LegacyImporterVersion}
	current := Generation{ImporterVersion: ImporterVersion}
	if got := generationPipelineStages(legacy); !reflect.DeepEqual(got, pipelineStages) {
		t.Fatalf("legacy stage order changed: %v", got)
	}
	stages := generationPipelineStages(current)
	if len(stages) != len(pipelineStages)+1 || stages[4] != "residential-context" || stages[3] != "postprocess" || stages[5] != "derive" {
		t.Fatalf("residential stage is not between postprocess and derivation: %v", stages)
	}
	pipeline := CommandPipeline{Root: filepath.Join("..", "..", "osm")}
	for _, stage := range pipelineStages {
		before, err := pipeline.StageFence(stage)
		if err != nil {
			t.Fatal(err)
		}
		after, err := pipeline.StageFenceForGeneration(stage, legacy)
		if err != nil || after != before {
			t.Fatalf("legacy fence changed for %s: %v", stage, err)
		}
	}
	for _, stage := range []string{"tags-filter", "osm2pgsql"} {
		before, _ := pipeline.StageFenceForGeneration(stage, legacy)
		after, err := pipeline.StageFenceForGeneration(stage, current)
		if err != nil || after == before {
			t.Fatalf("importer-v4 source contract not fenced for %s: %v", stage, err)
		}
	}
	legacyFence, _ := pipeline.StageFenceForGeneration("identity-label-overrides", legacy)
	currentFence, err := pipeline.StageFenceForGeneration("identity-label-overrides", current)
	if err != nil || legacyFence == currentFence || pipelineSQLFile("identity-label-overrides", legacy) != "identity-label-overrides-batched.sql" || pipelineSQLFile("identity-label-overrides", current) != "identity-label-overrides-sequential-batched.sql" {
		t.Fatalf("generation-specific label access plans are not fenced: %v", err)
	}
	if task := logicalTask("residential-context", "frontage"); task.Order != 6 || !task.Repeated || len(UpdateTasks) != 58 {
		t.Fatalf("context processing changed the historical public task identities: %+v", task)
	}
}

func TestResidentialImporterUsesGenerationSpecificFilterAndStyle(t *testing.T) {
	for _, version := range []int{LegacyImporterVersion, ImporterVersion} {
		var commands []string
		pipeline := CommandPipeline{Root: "/app/osm", DatabaseURL: "postgresql://writer@fixture/osm",
			outputCommand: func(_ context.Context, environment []string, name string, args ...string) (string, error) {
				commands = append(commands, name+" "+strings.Join(args, " ")+" "+strings.Join(environment, " "))
				return "", nil
			},
		}
		generation := Generation{ID: 1, SchemaName: "osm_build_1", ImporterVersion: version}
		for _, stage := range []string{"tags-filter", "osm2pgsql", "identity-label-overrides"} {
			if _, err := pipeline.Run(context.Background(), stage, generation, "source.pbf", "filtered.pbf"); err != nil {
				t.Fatal(err)
			}
		}
		joined := strings.Join(commands, "\n")
		for _, fragment := range []string{"w/building", "r/building", "n/addr:housenumber", "w/landuse=residential", "import-residential.lua"} {
			if strings.Contains(joined, fragment) != (version >= 4) {
				t.Errorf("importer %d has incorrect source selection for %s", version, fragment)
			}
		}
		if strings.Contains(joined, "identity-label-overrides-sequential-batched.sql") != (version >= 4) {
			t.Errorf("importer %d selected the wrong identity-label SQL", version)
		}
	}
}
