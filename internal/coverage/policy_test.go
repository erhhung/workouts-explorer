package coverage

import (
	"encoding/json"
	"testing"
)

func TestWorkoutAwarePathEligibility(t *testing.T) {
	tests := []struct {
		name    string
		mode    MovementMode
		highway string
		tags    string
		want    bool
	}{
		{"foot residential", MovementFoot, "residential", `{}`, true},
		{"foot private path", MovementFoot, "path", `{"access":"private"}`, true},
		{"foot forbidden path", MovementFoot, "path", `{"access":"no"}`, false},
		{"foot explicitly forbidden private path", MovementFoot, "path", `{"access":"private","foot":"no"}`, false},
		{"bicycle private path", MovementBicycle, "path", `{"access":"private"}`, true},
		{"foot private neighborhood road attribution", MovementFoot, "residential", `{"access":"private"}`, true},
		{"bicycle private neighborhood road attribution", MovementBicycle, "service", `{"access":"private"}`, true},
		{"foot private driveway rejected", MovementFoot, "service", `{"access":"private","service":"driveway"}`, false},
		{"foot named private HOA road", MovementFoot, "service", `{"name":"Cameron Drive","access":"private","service":"driveway"}`, true},
		{"foot named public driveway remains rejected", MovementFoot, "service", `{"name":"Example","service":"driveway"}`, false},
		{"foot motorway default", MovementFoot, "motorway", `{}`, false},
		{"foot motorway override", MovementFoot, "motorway", `{"foot":"yes"}`, true},
		{"bicycle cycleway", MovementBicycle, "cycleway", `{}`, true},
		{"bicycle prohibited", MovementBicycle, "path", `{"bicycle":"no"}`, false},
		{"bicycle steps default", MovementBicycle, "steps", `{}`, false},
		{"bicycle steps dismount", MovementBicycle, "steps", `{"bicycle":"dismount"}`, true},
		{"shared motorway default", MovementSharedPublic, "motorway", `{}`, false},
		{"shared public path", MovementSharedPublic, "path", `{}`, true},
		{"construction rejected", MovementFoot, "construction", `{}`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := experimentalPathPolicy(test.mode).eligible(test.highway, json.RawMessage(test.tags)); got != test.want {
				t.Fatalf("eligible=%t, want %t", got, test.want)
			}
		})
	}
}

func TestWorkoutAwareDirections(t *testing.T) {
	tests := []struct {
		name           string
		mode           MovementMode
		tags           string
		motorF, motorR bool
		wantF, wantR   bool
	}{
		{"foot ignores motor oneway", MovementFoot, `{"oneway":"yes"}`, true, false, true, true},
		{"foot explicit reverse", MovementFoot, `{"oneway:foot":"-1"}`, true, true, false, true},
		{"bicycle motor oneway", MovementBicycle, `{"oneway":"yes"}`, true, false, true, false},
		{"bicycle override", MovementBicycle, `{"oneway":"yes","oneway:bicycle":"no"}`, true, false, true, true},
		{"shared remains bidirectional", MovementSharedPublic, `{"oneway":"yes"}`, true, false, true, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			forward, reverse := experimentalPathPolicy(test.mode).directions(json.RawMessage(test.tags), test.motorF, test.motorR)
			if forward != test.wantF || reverse != test.wantR {
				t.Fatalf("directions=(%t,%t), want (%t,%t)", forward, reverse, test.wantF, test.wantR)
			}
		})
	}
}
