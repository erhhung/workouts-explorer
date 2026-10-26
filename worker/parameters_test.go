package worker

import (
	"fmt"
	"testing"
	"time"
)

func TestDecodeIngestParameters(t *testing.T) {
	const source = "0123456789ABCDEF0123456789ABCDEF"
	tests := []struct {
		name    string
		raw     string
		mode    ingestMode
		invalid bool
	}{
		{"incremental", `{"sourceId":"` + source + `","generation":1,"mode":"incremental"}`, ingestIncremental, false},
		{"bounded", `{"sourceId":"` + source + `","generation":2,"mode":"bounded","startDate":"2026-01-01","endDate":"2026-01-31"}`, ingestBounded, false},
		{"legacy schema 6", `{"sourceId":"` + source + `","generation":1,"mode":"bounded","startDate":"0001-01-01","endDate":"9999-12-31","legacySchema6":true}`, ingestBounded, false},
		{"legacy marker is bounded", `{"sourceId":"` + source + `","generation":1,"mode":"bounded","startDate":"2026-01-01","endDate":"2026-01-31","legacySchema6":true}`, "", true},
		{"legacy marker must be true", `{"sourceId":"` + source + `","generation":1,"mode":"bounded","startDate":"0001-01-01","endDate":"9999-12-31","legacySchema6":false}`, "", true},
		{"legacy marker cannot be incremental", `{"sourceId":"` + source + `","generation":1,"mode":"incremental","legacySchema6":true}`, "", true},
		{"missing mode", `{"sourceId":"` + source + `","generation":1}`, "", true},
		{"missing paired date", `{"sourceId":"` + source + `","generation":1,"mode":"bounded","startDate":"2026-01-01"}`, "", true},
		{"reversed", `{"sourceId":"` + source + `","generation":1,"mode":"bounded","startDate":"2026-02-01","endDate":"2026-01-01"}`, "", true},
		{"invalid calendar date", `{"sourceId":"` + source + `","generation":1,"mode":"bounded","startDate":"2026-02-30","endDate":"2026-03-01"}`, "", true},
		{"unknown field", `{"sourceId":"` + source + `","generation":1,"mode":"incremental","path":"private"}`, "", true},
		{"lowercase source", `{"sourceId":"0123456789abcdef0123456789abcdef","generation":1,"mode":"incremental"}`, "", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mode, start, end, err := decodeIngestParameters([]byte(test.raw))
			if (err != nil) != test.invalid || mode != test.mode {
				t.Fatalf("mode=%q err=%v", mode, err)
			}
			if test.invalid || mode == ingestIncremental {
				if start != nil || end != nil {
					t.Fatalf("unexpected dates: %v/%v", start, end)
				}
			} else if start == nil || end == nil || end.Before(*start) {
				t.Fatalf("bounded dates: %v/%v", start, end)
			}
		})
	}
}

func TestDecodeIngestReprocessingParameters(t *testing.T) {
	const source = `"sourceId":"0123456789ABCDEF0123456789ABCDEF","generation":1`
	const dates = `,"startDate":"2026-01-01","endDate":"2026-01-31"`
	for _, mode := range []string{"bounded", "incremental", "missing", "unknown"} {
		for _, flag := range []string{"absent", "false", "true", "null", `"false"`, `"true"`, "0", "1", "[]", "{}", "TRUE", "False"} {
			t.Run(mode+"/"+flag, func(t *testing.T) {
				raw := "{" + source
				if mode != "missing" {
					raw += fmt.Sprintf(`,"mode":%q`, mode)
				}
				if mode == "bounded" {
					raw += dates
				}
				if flag != "absent" {
					raw += `,"reprocessAllFiles": ` + flag + " "
				}
				raw += "}"
				wantValid := mode == "bounded" && (flag == "absent" || flag == "false" || flag == "true") ||
					mode == "incremental" && flag == "absent"
				got, start, end, err := decodeIngestParameters([]byte(raw))
				if (err == nil) != wantValid {
					t.Fatalf("mode=%q dates=%v/%v err=%v, want valid=%t", got, start, end, err, wantValid)
				}
				if wantValid && got != ingestMode(mode) {
					t.Fatalf("mode=%q, want %q", got, mode)
				}
				if wantValid && mode == "bounded" && (start == nil || end == nil ||
					start.Format(time.DateOnly) != "2026-01-01" || end.Format(time.DateOnly) != "2026-01-31") {
					t.Fatalf("bounded dates=%v/%v", start, end)
				}
			})
		}
	}
	for _, flag := range []string{"false", "true", "null"} {
		t.Run("legacy schema 6/"+flag, func(t *testing.T) {
			raw := "{" + source + `,"mode":"bounded","startDate":"0001-01-01","endDate":"9999-12-31","legacySchema6":true,"reprocessAllFiles":` + flag + "}"
			if _, _, _, err := decodeIngestParameters([]byte(raw)); err == nil {
				t.Fatal("legacy schema 6 accepted new flag")
			}
		})
	}
	for _, extra := range []string{`,"ingestContractVersion":1`, `,"ingestContractVersion":null`} {
		if _, _, _, err := decodeIngestParameters([]byte("{" + source + `,"mode":"bounded"` + dates + `,"reprocessAllFiles":false` + extra + "}")); err == nil {
			t.Fatal("accepted ingest contract version marker")
		}
	}
}
