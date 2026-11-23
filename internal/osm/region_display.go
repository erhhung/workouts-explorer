package osm

import (
	"html"
	"regexp"
	"strings"
	"unicode"
)

var providerHTML = regexp.MustCompile(`<[^>]*>`)
var regionWords = regexp.MustCompile(`(?i:a\.k\.a\.)|[\p{L}\p{M}\p{N}]+`)
var regionMinorWords = map[string]bool{
	"a": true, "an": true, "and": true, "as": true, "at": true, "by": true,
	"for": true, "from": true, "in": true, "of": true, "on": true, "or": true,
	"the": true, "to": true, "with": true,
	"mit": true, "et": true, "y": true, "includes": true, "a.k.a.": true,
	"à": true, "au": true, "aux": true, "d": true, "da": true, "das": true,
	"de": true, "del": true, "della": true, "delle": true, "des": true,
	"di": true, "do": true, "dos": true, "du": true, "el": true, "en": true,
	"l": true, "la": true, "las": true, "le": true, "les": true, "los": true,
	"van": true, "von": true, "der": true, "den": true,
}

// Provider labels are plain text. Apply word-level title casing, not a region
// name allowlist, while retaining punctuation and existing internal capitals.
func DisplayRegionName(value string) string {
	value = providerHTML.ReplaceAllString(html.UnescapeString(value), " ")
	value = strings.Join(strings.Fields(value), " ")
	if strings.Contains(value, "/") {
		parts := strings.Split(value, "/")
		for i, part := range parts {
			parts[i] = strings.TrimSpace(strings.ReplaceAll(part, "-", " "))
		}
		value = strings.Join(parts, " / ")
	}
	var result strings.Builder
	previous, first := 0, true
	for _, bounds := range regionWords.FindAllStringIndex(value, -1) {
		separator := value[previous:bounds[0]]
		if strings.ContainsAny(separator, "/:") {
			first = true
		}
		result.WriteString(separator)
		word := value[bounds[0]:bounds[1]]
		lower := strings.ToLower(word)
		switch {
		case lower == "a.k.a.":
			word = lower
			if first {
				word = "A.k.a."
			}
		case !first && regionMinorWords[lower]:
			word = lower
		case lower == "us" || lower == "uk" || lower == "usa" || lower == "uae" || lower == "eu" || lower == "dc":
			word = strings.ToUpper(word)
		default:
			runes := []rune(word)
			runes[0] = unicode.ToUpper(runes[0])
			word = string(runes)
		}
		result.WriteString(word)
		previous, first = bounds[1], false
	}
	result.WriteString(value[previous:])
	return result.String()
}
