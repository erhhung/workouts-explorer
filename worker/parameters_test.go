package worker

import "testing"

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
			mode, _, _, err := decodeIngestParameters([]byte(test.raw))
			if (err != nil) != test.invalid || mode != test.mode {
				t.Fatalf("mode=%q err=%v", mode, err)
			}
		})
	}
}
