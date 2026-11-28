package osm

import "testing"

func TestRegionProcessingStatusTracksMapContractNotMatcherPolicy(t *testing.T) {
	for _, test := range []struct {
		name                 string
		promoted             bool
		importer, derivation int
		want                 string
	}{
		{"no map", false, 0, 0, "Available"},
		{"legacy importer", true, LegacyImporterVersion, DerivationVersion, "Stale"},
		{"legacy derivation", true, ImporterVersion, DerivationVersion - 1, "Stale"},
		{"current map contract", true, ImporterVersion, DerivationVersion, "Ready"},
		{"newer compatible map contract", true, ImporterVersion + 1, DerivationVersion + 1, "Ready"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := RegionProcessingStatus(test.promoted, test.importer, test.derivation); got != test.want {
				t.Fatalf("status=%s want=%s", got, test.want)
			}
		})
	}
}
