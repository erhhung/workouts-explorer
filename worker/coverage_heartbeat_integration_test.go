package worker

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestCoverageRunnerRunOnceDoesNotHeartbeatItsOwnSlowCommitIntegration(t *testing.T) {
	f := newCoverageIntegrationFixture(t)
	f.seedOSM()
	workout := f.addWorkout(coverageIntegrationRoute(0))
	job := f.queue(workout)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	name := "coverage_slow_commit_" + suffix
	function := pgx.Identifier{"app", name}.Sanitize()
	trigger := pgx.Identifier{name}.Sanitize()
	if _, err := f.adminDB.Exec(f.ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $sleep$
	BEGIN
	 IF NEW.id='%s'::uuid AND OLD.status='running' AND NEW.status='succeeded' THEN
	  PERFORM pg_sleep(6);
	 END IF;
	 RETURN NULL;
	END;$sleep$;
	CREATE CONSTRAINT TRIGGER %s AFTER UPDATE ON app.jobs DEFERRABLE INITIALLY DEFERRED
	FOR EACH ROW EXECUTE FUNCTION %s();`, function, job.childID, trigger, function)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := f.adminDB.Exec(ctx, `DROP TRIGGER `+trigger+` ON app.jobs;DROP FUNCTION `+function+`();`); err != nil {
			t.Error(err)
		}
	})
	runner := f.runner()
	runner.heartbeatInterval = 20 * time.Millisecond
	// The final COMMIT deliberately outlasts heartbeat lock/control timeouts.
	// The existing lease and operation deadline still bound the entire write.
	started := time.Now()
	worked, err := runner.RunOnce(f.ctx)
	if !worked || err != nil {
		t.Fatalf("RunOnce worked=%t error=%v", worked, err)
	}
	if time.Since(started) < 6*time.Second {
		t.Fatal("fixture did not hold the job-row lock through the slow commit")
	}
	f.assertTerminal(job, "succeeded", "applied")
	f.assertCurrent(workout, job, 0, 1)
}
