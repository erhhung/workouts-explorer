package osm

import (
	"strings"
	"testing"
)

func TestRegionMinorWordsAndAbbreviations(t *testing.T) {
	for _, word := range []string{"mit", "et", "y", "with", "de", "da", "la", "includes"} {
		if got := DisplayRegionName("alpha " + strings.ToUpper(word) + " beta"); got != "Alpha "+word+" Beta" {
			t.Errorf("minor word %q: %q", word, got)
		}
		want := strings.ToUpper(word[:1]) + word[1:] + " Beta"
		if got := DisplayRegionName(word + " beta"); got != want {
			t.Errorf("initial word %q: %q want %q", word, got, want)
		}
	}
	for input, want := range map[string]string{
		"alpha a.k.a. beta":     "Alpha a.k.a. Beta",
		"alpha A.K.A. beta":     "Alpha a.k.a. Beta",
		"a.k.a. beta":           "A.k.a. Beta",
		"alpha (a.k.a. beta)":   "Alpha (a.k.a. Beta)",
		"alpha (includes beta)": "Alpha (includes Beta)",
	} {
		if got := DisplayRegionName(input); got != want {
			t.Errorf("abbreviation %q: %q want %q", input, got, want)
		}
	}
}

func TestGenericRegionTitleCase(t *testing.T) {
	for input, want := range map[string]string{
		"Polynésie française (French Polynesia)": "Polynésie Française (French Polynesia)",
		"île-de-france":               "Île-de-France",
		"Côte d'Ivoire":               "Côte d'Ivoire",
		"isle of man":                 "Isle of Man",
		"Trinidad and Tobago":         "Trinidad and Tobago",
		"McMurdo and USA":             "McMurdo and USA",
		"the hague (the netherlands)": "The Hague (the Netherlands)",
	} {
		if got := DisplayRegionName(input); got != want {
			t.Errorf("display=%q want %q", got, want)
		}
	}
}

func TestLocalizedAdministrativeRegionNames(t *testing.T) {
	for input, want := range map[string]string{
		"Województwo kujawsko-pomorskie (Kuyavian-Pomeranian Voivodeship)": "Województwo Kujawsko-Pomorskie (Kuyavian-Pomeranian Voivodeship)",
		"Ústecký kraj": "Ústecký Kraj",
		"Královéhradecký kraj (Hradec Králové Region)": "Královéhradecký Kraj (Hradec Králové Region)",
	} {
		if got := DisplayRegionName(input); got != want {
			t.Errorf("display=%q want %q", got, want)
		}
	}
}

func TestDisplayRegionName(t *testing.T) {
	for input, want := range map[string]string{
		"us/district-of-columbia":               "US / District of Columbia",
		"Trøndelag<br />(Central Norway)":       "Trøndelag (Central Norway)",
		"Trøndelag&lt;br /&gt;(Central Norway)": "Trøndelag (Central Norway)",
		"Bosnia &amp; Herzegovina":              "Bosnia & Herzegovina",
		"Northern California":                   "Northern California",
		"Baden-Württemberg":                     "Baden-Württemberg",
		"  North<br>  America\n":                "North America",
	} {
		t.Run(input, func(t *testing.T) {
			if got := DisplayRegionName(input); got != want {
				t.Fatalf("display=%q want %q", got, want)
			}
		})
	}
}
