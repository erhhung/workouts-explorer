package healthautoexport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// normalizedTypeKey uses trimmed NFKC plus Unicode case folding as its
// canonical label. Its readable letter/digit slug is suffixed with the first
// 128 bits of SHA-256(canonical label), preventing slug collisions while making
// canonically equivalent labels share a stable key. The slug is truncated only
// at rune boundaries so the complete key is at most MaxTypeKeyBytes bytes.
func normalizedTypeKey(label string) string {
	canonical := norm.NFKC.String(cases.Fold().String(norm.NFKC.String(strings.TrimSpace(label))))
	var slug strings.Builder
	const maxSlugBytes = MaxTypeKeyBytes - 1 - 32
	separator := false
	for _, r := range canonical {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			runeBytes := len(string(r))
			separatorBytes := 0
			if separator && slug.Len() > 0 {
				separatorBytes = 1
			}
			if slug.Len()+separatorBytes+runeBytes > maxSlugBytes {
				break
			}
			if separatorBytes != 0 {
				slug.WriteByte('-')
			}
			slug.WriteRune(r)
			separator = false
		} else {
			separator = true
		}
	}
	if slug.Len() == 0 {
		slug.WriteString("workout")
	}
	digest := sha256.Sum256([]byte(canonical))
	return slug.String() + "-" + hex.EncodeToString(digest[:16])
}

type canonicalWorkout struct {
	ProviderID       string
	ProviderLabel    string
	TypeKey          string
	StartUnixNano    int64
	EndUnixNano      int64
	StartOffsetMins  int
	EndOffsetMins    int
	LocalStartDate   string
	ProviderDuration Decimal
	IsIndoor         *bool
	Location         *string
	Aggregates       []Aggregate
	Route            []RoutePoint
}

func canonicalContent(workout Workout) []byte {
	canonical := canonicalWorkout{
		ProviderID: workout.ProviderID, ProviderLabel: workout.ProviderLabel, TypeKey: workout.TypeKey,
		StartUnixNano: workout.Start.UnixNano(), EndUnixNano: workout.End.UnixNano(),
		StartOffsetMins: workout.StartOffsetMins, EndOffsetMins: workout.EndOffsetMins,
		LocalStartDate: workout.LocalStartDate.Format("2006-01-02"), ProviderDuration: workout.ProviderDuration,
		IsIndoor: workout.IsIndoor, Location: workout.Location, Aggregates: workout.Aggregates, Route: workout.Route,
	}
	encoded, _ := json.Marshal(canonical)
	return encoded
}

func contentHash(workout Workout) [32]byte {
	return sha256.Sum256(append([]byte(ContentHashVersion+"\n"), canonicalContent(workout)...))
}

func fallbackHash(workout Workout) [32]byte {
	// Stable v2 wire format:
	// SHA-256(version || NUL || type key || NUL || UTC start Unix nanoseconds).
	// The normalized type and start instant are the smallest defensible provider
	// identity tuple. Mutable end, duration, offset, metrics, and route content
	// are deliberately excluded. Persistence additionally scopes by source.
	identity := make([]byte, 0, len(FallbackFingerprintVersion)+len(workout.TypeKey)+32)
	identity = append(identity, FallbackFingerprintVersion...)
	identity = append(identity, 0)
	identity = append(identity, workout.TypeKey...)
	identity = append(identity, 0)
	identity = strconv.AppendInt(identity, workout.Start.UnixNano(), 10)
	return sha256.Sum256(identity)
}
