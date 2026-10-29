package worker

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestReconciliationDefersOnlyTypedRouteFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"ROI deadline", &osm.RouteRegionResolutionError{Err: context.DeadlineExceeded, DeadlineExceeded: true}, "roi_deadline"},
		{"ROI query deadline", &osm.RouteRegionResolutionError{Err: &pgconn.PgError{Code: "57014"}, DeadlineExceeded: true}, "roi_deadline"},
		{"ROI invalid row", &osm.RouteRegionResolutionError{Err: pgx.ScanArgError{Err: errors.New("invalid result field")}, InvalidData: true}, "roi_invalid_data"},
		{"ROI invalid count", &osm.RouteRegionResolutionError{Err: osm.ErrRouteRegionResultInvalid, InvalidData: true}, "roi_invalid_data"},
		{"ROI data SQL", &osm.RouteRegionResolutionError{Err: &pgconn.PgError{Code: "22P02"}, InvalidData: true}, "roi_invalid_data"},
		{"global deadline", context.DeadlineExceeded, ""},
		{"global missing metadata", errors.New("missing catalog metadata"), ""},
		{"query cancellation", &osm.RouteRegionResolutionError{Err: &pgconn.PgError{Code: "57014"}}, ""},
		{"parent cancellation", &osm.RouteRegionResolutionError{Err: context.Canceled, DeadlineExceeded: true}, ""},
		{"lease lost", &osm.RouteRegionResolutionError{Err: errLeaseLost, DeadlineExceeded: true}, ""},
		{"replica lag", &osm.RouteRegionResolutionError{Err: osm.ErrMatcherReplicaNotReady, DeadlineExceeded: true}, ""},
		{"permission", &osm.RouteRegionResolutionError{Err: errors.Join(context.DeadlineExceeded, &pgconn.PgError{Code: "42501"}), DeadlineExceeded: true}, ""},
		{"transport", &osm.RouteRegionResolutionError{Err: errors.Join(context.DeadlineExceeded, io.EOF), DeadlineExceeded: true}, ""},
		{"connection SQL", &osm.RouteRegionResolutionError{Err: &pgconn.PgError{Code: "08006"}, DeadlineExceeded: true}, ""},
		{"system SQL", &osm.RouteRegionResolutionError{Err: &pgconn.PgError{Code: "XX000"}, InvalidData: true}, ""},
		{"nil tag", (*osm.RouteRegionResolutionError)(nil), ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := deferredRouteResolutionReason(context.Background(), test.err); got != test.want {
				t.Fatalf("reason=%q want=%q", got, test.want)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := deferredRouteResolutionReason(ctx, &osm.RouteRegionResolutionError{Err: context.DeadlineExceeded, DeadlineExceeded: true}); got != "" {
		t.Fatal("cancelled parent deferred a route")
	}
}
