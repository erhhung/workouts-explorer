package workouttimezone

import (
	"context"
	"time"
	_ "time/tzdata"

	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const RouteBoundarySource = "route_boundary"

type Resolution struct {
	Name    *string
	Source  *string
	Release *string
}

// ResolveRoute uses the provider's first route coordinate and rejects a zone
// whose historical offset conflicts with the offset preserved in the export.
func ResolveRoute(ctx context.Context, pool *pgxpool.Pool, startedAt time.Time, offsetMinutes *int, route []healthautoexport.RoutePoint) (Resolution, error) {
	if pool == nil || len(route) == 0 {
		return Resolution{}, nil
	}
	var name, release string
	err := pool.QueryRow(ctx, `
		SELECT zone.name,dataset.release
		  FROM osm_catalog.timezone_datasets dataset
		 CROSS JOIN LATERAL (SELECT osm_active.timezone_at($1,$2) AS name) zone
		 WHERE dataset.state='active' AND zone.name IS NOT NULL`, route[0].Longitude, route[0].Latitude).Scan(&name, &release)
	if err == pgx.ErrNoRows {
		return Resolution{}, nil
	}
	if err != nil {
		return Resolution{}, err
	}
	if !MatchesRecordedOffset(name, startedAt, offsetMinutes) {
		return Resolution{}, nil
	}
	source := RouteBoundarySource
	return Resolution{Name: &name, Source: &source, Release: &release}, nil
}

func MatchesRecordedOffset(name string, instant time.Time, offsetMinutes *int) bool {
	location, err := time.LoadLocation(name)
	if err != nil {
		return false
	}
	if offsetMinutes == nil {
		return true
	}
	_, seconds := instant.In(location).Zone()
	return seconds == *offsetMinutes*60
}
