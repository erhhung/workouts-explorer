package worker

import (
	"log/slog"
	"testing"
)

func TestCoverageReconcilerReadReplicaOption(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		reconciler := NewCoverageReconciler(nil, nil, slog.Default(), CoverageReconcilerOptions{UseReadReplicasOnly: enabled})
		if reconciler.useReadReplicasOnly != enabled {
			t.Fatalf("reader-only=%v, want %v", reconciler.useReadReplicasOnly, enabled)
		}
	}
}
