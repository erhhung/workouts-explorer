package osmcontroller

import "testing"

func TestTerminationSummariesDoNotRepeatTheTaskName(t *testing.T) {
	for _, test := range []struct {
		failure ExecutionFailure
		want    string
	}{
		{ExecutionFailure{Reason: "Error", ExitCode: 1}, "Updater container exited with code 1. Review the updater pod logs for this job."},
		{ExecutionFailure{Reason: "Evicted"}, "Kubernetes evicted the updater pod. Check node memory and scratch capacity."},
		{ExecutionFailure{Reason: "OOMKilled", ExitCode: 137}, "Updater exceeded its configured memory limit (OOMKilled, exit 137)."},
	} {
		if got := test.failure.Summary(); got != test.want {
			t.Fatalf("summary=%q, want %q", got, test.want)
		}
	}
}
