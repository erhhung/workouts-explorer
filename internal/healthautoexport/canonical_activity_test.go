package healthautoexport

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestCanonicalActivityLabel(t *testing.T) {
	for _, prefix := range []string{"Indoor", "Outdoor"} {
		for canonical, aliases := range map[string][]string{
			"Run":   {"Run", "Running"},
			"Walk":  {"Walk", "Walking"},
			"Cycle": {"Cycle", "Cycling", "Bike", "Biking", "Bicycle", "Bicycling"},
		} {
			for _, alias := range aliases {
				for _, label := range []string{prefix + " " + alias, prefix + alias, " \t" + strings.ToUpper(prefix) + "\u00a0 \n" + strings.ToLower(alias) + " "} {
					t.Run(label, func(t *testing.T) {
						want := prefix + " " + canonical
						if got := CanonicalActivityLabel(label); got != want || CanonicalActivityLabel(got) != got {
							t.Fatalf("display=%q want=%q", got, want)
						}
					})
				}
			}
		}
	}
	if got := CanonicalActivityLabel("\uff2f\uff55\uff54\uff44\uff4f\uff4f\uff52\u3000\uff23\uff59\uff43\uff4c\uff49\uff4e\uff47"); got != "Outdoor Cycle" {
		t.Fatalf("NFKC display=%q", got)
	}
}

func TestCanonicalActivityLabelPreservesUnknown(t *testing.T) {
	for _, label := range []string{"", "Running", "Walking", "Cycling", "Bike", "Bicycle", " Run ", "Climbing", "Outdoor Hiking", "Indoor Rowing", "Outdoor Run Intervals", "Indoor Cycling Class", "Outdoor-Cycling", "Cycling Outdoor", "Out doorRun", "Indoor Run ning", "Private provider label\t", "\uff23\uff59\uff43\uff4c\uff49\uff4e\uff47"} {
		if got := CanonicalActivityLabel(label); got != label {
			t.Errorf("unknown %q rewritten as %q", label, got)
		}
	}
}

func TestCanonicalActivityLabelDoesNotChangeParserIdentity(t *testing.T) {
	for _, label := range []string{"Outdoor Running", "Outdoor Walking", "Outdoor Cycling", "IndoorRunning", "IndoorWalking", "IndoorBiking"} {
		for _, providerID := range []string{"", "opaque/provider:v1", "0ACE8792-3C73-4554-83A4-724434A75279"} {
			t.Run(label+"/"+providerID, func(t *testing.T) {
				raw := fmt.Sprintf(`{"id":%q,"name":%q,"start":"2026-01-02 03:04:05 -0700","end":"2026-01-02 03:05:05 -0700","duration":60,"isIndoor":null}`, providerID, label)
				first := parseOne(t, raw)
				before := canonicalContent(first)
				if got := CanonicalActivityLabel(first.ProviderLabel); got == label {
					t.Fatalf("fixture alias was not recognized: %q", got)
				}
				second := parseOne(t, raw)
				if !reflect.DeepEqual(first, second) || first.ProviderLabel != label || first.ProviderID != providerID ||
					first.TypeKey != NormalizedTypeKey(label) || first.IsIndoor != nil || !bytes.Equal(before, canonicalContent(first)) ||
					first.ContentSHA256 != contentHash(first) {
					t.Fatal("display canonicalization changed provider model or content identity")
				}
				if providerID == "" && first.FallbackSHA256 != fallbackHash(first) {
					t.Fatal("display canonicalization changed fallback v2 identity")
				}
				canonical := first
				canonical.ProviderLabel = CanonicalActivityLabel(first.ProviderLabel)
				canonical.TypeKey = NormalizedTypeKey(canonical.ProviderLabel)
				if canonical.TypeKey == first.TypeKey || contentHash(canonical) == first.ContentSHA256 ||
					providerID == "" && fallbackHash(canonical) == first.FallbackSHA256 {
					t.Fatal("display aliases must not be substituted into parser identity")
				}
			})
		}
	}
}
