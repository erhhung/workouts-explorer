package main

import "testing"

func TestParseOptionalID(t *testing.T) {
	for _, test := range []struct {
		value string
		want  int64
		valid bool
	}{
		{value: "", want: 0, valid: true},
		{value: "42", want: 42, valid: true},
		{value: "0", want: 0, valid: true},
		{value: "-1", valid: false},
		{value: "not-an-id", valid: false},
	} {
		got, err := parseOptionalID(test.value)
		if (err == nil) != test.valid || got != test.want {
			t.Errorf("parseOptionalID(%q)=(%d,%v), want (%d, valid=%v)", test.value, got, err, test.want, test.valid)
		}
	}
}
