package healthautoexport

import (
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// CanonicalActivityLabel returns a display label, not a provider identity. Only
// explicitly indoor/outdoor run, walk, and cycle aliases are recognized; unknown
// and unqualified labels are preserved byte-for-byte.
func CanonicalActivityLabel(label string) string {
	name := strings.Join(strings.Fields(cases.Fold().String(norm.NFKC.String(label))), " ")
	var prefix string
	switch {
	case strings.HasPrefix(name, "indoor"):
		prefix, name = "Indoor", strings.TrimSpace(strings.TrimPrefix(name, "indoor"))
	case strings.HasPrefix(name, "outdoor"):
		prefix, name = "Outdoor", strings.TrimSpace(strings.TrimPrefix(name, "outdoor"))
	default:
		return label
	}
	switch name {
	case "run", "running":
		return prefix + " Run"
	case "walk", "walking":
		return prefix + " Walk"
	case "cycle", "cycling", "bike", "biking", "bicycle", "bicycling":
		return prefix + " Cycle"
	default:
		return label
	}
}
