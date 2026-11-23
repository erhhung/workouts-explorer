package main

import (
	"context"
	"errors"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/osm"
)

type maintenanceProgress struct {
	events []osm.UpdateProgress
}

func (p *maintenanceProgress) Report(_ context.Context, event osm.UpdateProgress) error {
	p.events = append(p.events, event)
	return nil
}

type maintenanceStore struct {
	progress *maintenanceProgress
	fail     string
}

func (s maintenanceStore) check(timing string) error {
	for _, event := range s.progress.events {
		if event.Completed {
			return errors.New("maintenance began after reporting completion")
		}
	}
	if len(s.progress.events) == 0 || s.progress.events[0].Type != "start" || s.progress.events[0].Task != len(osm.UpdateTasks) {
		return errors.New("maintenance did not reopen the final task")
	}
	if s.fail == timing {
		return errors.New("fixture maintenance failed")
	}
	return nil
}

func (s maintenanceStore) ProcessStorageGC(context.Context) error  { return s.check("cleanup") }
func (s maintenanceStore) RefreshStatistics(context.Context) error { return s.check("statistics") }

func TestPromotedMaintenanceReopensFinalTaskAndCompletesOnlyOnSuccess(t *testing.T) {
	for _, failure := range []string{"", "cleanup", "statistics"} {
		t.Run(failure, func(t *testing.T) {
			progress := &maintenanceProgress{}
			err := finishPromotedMaintenance(context.Background(), progress, maintenanceStore{progress: progress, fail: failure})
			if (err != nil) != (failure != "") {
				t.Fatal(err)
			}
			last := progress.events[len(progress.events)-1]
			if last.Completed != (failure == "") || (last.Completed && last.Type != "settled") {
				t.Fatal("incorrect final completion boundary", last)
			}
		})
	}
}
