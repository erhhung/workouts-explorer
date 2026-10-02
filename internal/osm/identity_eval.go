package osm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type IdentityEvaluationConfig struct {
	OSM               *pgxpool.Pool
	Application       *pgxpool.Pool
	OSMDatabaseURL    string
	PipelineRoot      string
	RegionID          string
	Localities        []string
	FullRegion        bool
	MaximumSegments   int64
	KeepScratchSchema bool
	Log               io.Writer
	StoragePreflight  StoragePreflight
}

type IdentityEvaluationReport struct {
	SchemaVersion   int                         `json:"schemaVersion"`
	RuleVersion     int                         `json:"ruleVersion"`
	Baseline        IdentityEvaluationBaseline  `json:"baseline"`
	Identity        IdentityEvaluationSummary   `json:"identity"`
	Regressions     []IdentityRegressionResult  `json:"regressions"`
	ContextChecks   []IdentityContextResult     `json:"contextChecks"`
	Outliers        []IdentityComponentSummary  `json:"largestComponents"`
	UnnamedOutliers []IdentityComponentSummary  `json:"largestUnnamedComponents"`
	Projection      *IdentityProjectionSummary  `json:"applicationProjection,omitempty"`
	ScratchSchema   string                      `json:"scratchSchema,omitempty"`
	Durations       IdentityEvaluationDurations `json:"durations"`
}

type IdentityEvaluationBaseline struct {
	RegionID          string `json:"regionId"`
	GenerationID      int64  `json:"generationId"`
	DerivationVersion int    `json:"derivationVersion"`
}

type IdentityEvaluationSummary struct {
	Segments                   int64   `json:"segments"`
	BaselineIdentities         int64   `json:"baselineIdentities"`
	CandidateIdentities        int64   `json:"candidateIdentities"`
	SplitIdentities            int64   `json:"splitIdentities"`
	MergedIdentities           int64   `json:"mergedIdentities"`
	RequiredEdgeSplits         int64   `json:"remainingRequiredEdgeSplits"`
	AbsorbedSlivers            int64   `json:"localityScopeSliversAbsorbed"`
	AbsorbedSliverM            float64 `json:"localityScopeSliverLengthMeters"`
	LocalityAssignmentsChanged int64   `json:"localityAssignmentsChanged"`
}

type IdentityRegressionResult struct {
	Name          string  `json:"name"`
	Locality      string  `json:"locality"`
	Expectation   string  `json:"expectation"`
	SourceWayIDs  []int64 `json:"sourceWayIds"`
	IdentityCount int     `json:"identityCount"`
	Passed        bool    `json:"passed"`
}

type IdentityContextResult struct {
	Name               string  `json:"name"`
	ExpectedLocality   string  `json:"expectedLocality"`
	SourceWayIDs       []int64 `json:"sourceWayIds"`
	CoveredSegments    int64   `json:"coveredSegments"`
	MismatchedSegments int64   `json:"mismatchedSegments"`
	Passed             bool    `json:"passed"`
}

type IdentityComponentSummary struct {
	LogicalPathID string  `json:"logicalPathId"`
	Locality      string  `json:"locality"`
	Name          string  `json:"name"`
	Class         string  `json:"class"`
	Segments      int64   `json:"segments"`
	SourceWays    int64   `json:"sourceWays"`
	LengthKM      float64 `json:"lengthKm"`
}

type IdentityProjectionSummary struct {
	Accounts                     int `json:"accounts"`
	MatchedSegments              int `json:"matchedSegments"`
	CurrentEntities              int `json:"currentEntities"`
	ProjectedEntities            int `json:"projectedEntities"`
	CurrentWorkoutAttributions   int `json:"currentWorkoutAttributions"`
	ProjectedWorkoutAttributions int `json:"projectedWorkoutAttributions"`
	SplitEntities                int `json:"splitEntities"`
	MergedEntities               int `json:"mergedEntities"`
}

type IdentityEvaluationDurations struct {
	ScratchSetupMS int64 `json:"scratchSetupMs"`
	IdentityMS     int64 `json:"identityMs"`
	ProjectionMS   int64 `json:"projectionMs"`
	TotalMS        int64 `json:"totalMs"`
}

type identityRegression struct {
	name, locality, expectation string
	ways                        []int64
}

var defaultIdentityRegressions = []identityRegression{
	{name: "Stevens Creek Trail to Sleeper Park", locality: "Mountain View", expectation: "same", ways: []int64{199618015, 199618017}},
	{name: "Cupertino Meteor Drive", locality: "Cupertino", expectation: "same", ways: []int64{8931366, 514041311, 514041313}},
	{name: "Cupertino unnamed cycleway", locality: "Cupertino", expectation: "same", ways: []int64{1533743995, 1533743997, 1533743998}},
	{name: "Cupertino south footway branch", locality: "Cupertino", expectation: "different", ways: []int64{1533743995, 1420682072}},
	{name: "Cupertino north footway branch", locality: "Cupertino", expectation: "different", ways: []int64{1533743995, 798163117}},
	{name: "Parker Ranch Trail county-scope sliver", locality: "Santa Clara County", expectation: "same", ways: []int64{158922462, 1421917178}},
	{name: "Mountain View Grant Road divided carriageways and approach", locality: "Mountain View", expectation: "same", ways: []int64{5010393, 5010394, 417027104, 417027107, 417027113}},
	{name: "Mountain View Yorkshire Way road-cycleway continuation", locality: "Mountain View", expectation: "same", ways: []int64{8928169, 799582568}},
	{name: "Mountain View Franklin Avenue park crossing", locality: "Mountain View", expectation: "same", ways: []int64{8953535, 723201843}},
	{name: "Los Altos Foothill Expressway divided carriageways", locality: "Los Altos", expectation: "same", ways: []int64{23797638, 36767298, 25025733}},
	{name: "Cupertino East West and plain Homestead labels", locality: "Cupertino", expectation: "three", ways: []int64{8948219, 95412064, 343498590}},
	{name: "Los Altos West Homestead boundary sliver", locality: "Los Altos", expectation: "same", ways: []int64{8932692, 289295557, 289295555}},
	{name: "Mountain View East and West El Camino Real", locality: "Mountain View", expectation: "different", ways: []int64{4809373, 4809368}},
	{name: "Sunnyvale East and West El Camino Real", locality: "Sunnyvale", expectation: "different", ways: []int64{50108416, 50108418}},
}

var defaultIdentityContextChecks = []struct {
	name, locality string
	ways           []int64
}{
	{name: "Mountain View Heatherstone Way", locality: "Mountain View", ways: []int64{52241958}},
	{name: "Mountain View Yorkshire Way", locality: "Mountain View", ways: []int64{8928169, 799582568}},
}

func EvaluateIdentities(ctx context.Context, config IdentityEvaluationConfig) (_ IdentityEvaluationReport, err error) {
	started := time.Now()
	if config.OSM == nil || config.OSMDatabaseURL == "" || config.PipelineRoot == "" || config.RegionID == "" || config.MaximumSegments < 1 || config.StoragePreflight == nil {
		return IdentityEvaluationReport{}, fmt.Errorf("identity evaluation configuration is incomplete")
	}
	if !config.FullRegion && len(config.Localities) == 0 {
		return IdentityEvaluationReport{}, fmt.Errorf("at least one locality or full-region mode is required")
	}

	report := IdentityEvaluationReport{SchemaVersion: 1, RuleVersion: DerivationVersion}
	if err := config.OSM.QueryRow(ctx, `SELECT generation.id,generation.derivation_version
		FROM osm_catalog.region_storage storage JOIN osm_catalog.generations generation ON generation.id=storage.generation_id
		WHERE storage.region_id=$1 AND generation.state='active'`, config.RegionID).
		Scan(&report.Baseline.GenerationID, &report.Baseline.DerivationVersion); err != nil {
		return report, fmt.Errorf("read active OSM generation")
	}
	report.Baseline.RegionID = config.RegionID

	selection := `segment.region_id=$1 AND segment.generation_id=$2`
	arguments := []any{config.RegionID, report.Baseline.GenerationID}
	if !config.FullRegion {
		selection += ` AND (EXISTS (SELECT 1 FROM osm_active.localities locality
			WHERE locality.relation_id=segment.locality_relation_id AND locality.name=ANY($3))
			OR segment.source_way_id=ANY($4))`
		arguments = append(arguments, config.Localities, identityRegressionWays())
	}
	if err := config.OSM.QueryRow(ctx, `SELECT count(*) FROM osm_canonical.path_segments segment WHERE `+selection, arguments...).Scan(&report.Identity.Segments); err != nil {
		return report, fmt.Errorf("count identity evaluation segments")
	}
	if report.Identity.Segments == 0 {
		return report, fmt.Errorf("identity evaluation selection is empty")
	}
	if report.Identity.Segments > config.MaximumSegments {
		return report, fmt.Errorf("identity evaluation selection exceeds the segment limit (%d > %d)", report.Identity.Segments, config.MaximumSegments)
	}
	if _, err := config.StoragePreflight.Check(ctx); err != nil {
		return report, fmt.Errorf("identity evaluation reservation storage preflight: %w", err)
	}

	pipeline := CommandPipeline{DatabaseURL: config.OSMDatabaseURL, Root: config.PipelineRoot, Log: config.Log}
	versions, err := pipeline.Versions(ctx)
	if err != nil {
		return report, fmt.Errorf("validate identity evaluation tools: %w", err)
	}
	if versions.Osmium != ExpectedOsmium || versions.Osm2pgsql != ExpectedOsm2pgsql {
		return report, fmt.Errorf("unexpected identity evaluation tool versions")
	}
	store := PostgreSQLUpdateStore{Pool: config.OSM}
	evaluationGeneration, err := store.ReserveEvaluation(ctx, config.RegionID, versions)
	if err != nil {
		return report, fmt.Errorf("reserve identity evaluation generation: %w", err)
	}
	schema := evaluationGeneration.SchemaName
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if config.KeepScratchSchema {
		report.ScratchSchema = schema
	}
	defer func() {
		cleanupContext := context.Background()
		if config.KeepScratchSchema {
			return
		}
		cleanupErr := store.DropBuildSchema(cleanupContext, schema)
		if cleanupErr == nil {
			cleanupErr = store.Fail(cleanupContext, evaluationGeneration, "identity evaluation completed; scratch schema removed")
		}
		if cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("clean identity evaluation generation: %w", cleanupErr))
		}
	}()

	setupStarted := time.Now()
	segmentColumns := `segment_id,source_way_id,source_way_version,derivation_version,start_node_index,end_node_index,
		boundary_piece,start_graph_node_id,end_graph_node_id,name,normalized_name,highway,broad_class,tags,
		motor_forward_allowed,motor_reverse_allowed,geom,locality_relation_id,logical_path_id,length_m`
	if _, err := config.StoragePreflight.Check(ctx); err != nil {
		return report, fmt.Errorf("identity evaluation segment-copy storage preflight: %w", err)
	}
	if _, err := config.OSM.Exec(ctx, `CREATE UNLOGGED TABLE `+quotedSchema+`.path_segments AS SELECT `+segmentColumns+
		` FROM osm_canonical.path_segments segment WHERE `+selection, arguments...); err != nil {
		return report, fmt.Errorf("copy identity evaluation segments")
	}
	setupStatements := []string{
		`ALTER TABLE ` + quotedSchema + `.path_segments ADD PRIMARY KEY(segment_id)`,
		`CREATE INDEX ON ` + quotedSchema + `.path_segments(start_graph_node_id)`,
		`CREATE INDEX ON ` + quotedSchema + `.path_segments(end_graph_node_id)`,
		`CREATE UNLOGGED TABLE ` + quotedSchema + `.baseline_identity_map AS SELECT segment_id,logical_path_id FROM ` + quotedSchema + `.path_segments`,
		`CREATE INDEX ON ` + quotedSchema + `.baseline_identity_map(logical_path_id,segment_id)`,
		`CREATE TABLE ` + quotedSchema + `.logical_paths AS SELECT * FROM osm_active.logical_paths WITH NO DATA`,
		`CREATE TABLE ` + quotedSchema + `.localities AS SELECT relation_id,admin_level,name,geom FROM osm_active.localities`,
		`CREATE INDEX ON ` + quotedSchema + `.localities USING gist(geom)`,
		`CREATE TABLE ` + quotedSchema + `.park_areas(source_type text,source_id bigint,version integer,name text,normalized_name text,park_kind text,geom geometry,type_priority integer,area_m2 double precision)`,
		`CREATE TABLE ` + quotedSchema + `.education_areas(source_type text,source_id bigint,version integer,name text,normalized_name text,education_kind text,geom geometry,type_priority integer,area_m2 double precision)`,
	}
	for _, statement := range setupStatements {
		if _, err := config.StoragePreflight.Check(ctx); err != nil {
			return report, fmt.Errorf("identity evaluation setup storage preflight: %w", err)
		}
		if _, err := config.OSM.Exec(ctx, statement); err != nil {
			return report, fmt.Errorf("prepare identity evaluation schema")
		}
	}
	if _, err := config.StoragePreflight.Check(ctx); err != nil {
		return report, fmt.Errorf("identity evaluation locality-update storage preflight: %w", err)
	}
	result, err := config.OSM.Exec(ctx, `WITH county_segments AS (
		SELECT segment.segment_id,ST_LineInterpolatePoint(segment.geom,0.5) midpoint
		FROM `+quotedSchema+`.path_segments segment
		JOIN `+quotedSchema+`.localities current ON current.relation_id=segment.locality_relation_id
		WHERE current.admin_level=6
	), assigned AS (
		SELECT segment.segment_id,municipality.relation_id locality_relation_id
		FROM county_segments segment
		JOIN LATERAL (SELECT locality.relation_id FROM `+quotedSchema+`.localities locality
			WHERE locality.admin_level=8 AND locality.geom && segment.midpoint AND ST_Covers(locality.geom,segment.midpoint)
			ORDER BY locality.relation_id LIMIT 1) municipality ON true
	) UPDATE `+quotedSchema+`.path_segments segment SET locality_relation_id=assigned.locality_relation_id
	FROM assigned WHERE assigned.segment_id=segment.segment_id`)
	if err != nil {
		return report, fmt.Errorf("reassign identity evaluation localities")
	}
	report.Identity.LocalityAssignmentsChanged = result.RowsAffected()
	report.Durations.ScratchSetupMS = time.Since(setupStarted).Milliseconds()

	identityStarted := time.Now()
	if err := runIdentityEvaluationStages(ctx, store, pipeline, config.StoragePreflight, evaluationGeneration); err != nil {
		return report, err
	}
	report.Durations.IdentityMS = time.Since(identityStarted).Milliseconds()

	if err := readIdentityEvaluation(ctx, config.OSM, quotedSchema, &report); err != nil {
		return report, err
	}
	if config.Application != nil {
		projectionStarted := time.Now()
		if err := projectIdentityEvaluation(ctx, config.Application, config.OSM, quotedSchema, report.Baseline, &report); err != nil {
			return report, err
		}
		report.Durations.ProjectionMS = time.Since(projectionStarted).Milliseconds()
	}
	report.Durations.TotalMS = time.Since(started).Milliseconds()
	return report, nil
}

func runIdentityEvaluationStages(ctx context.Context, store PostgreSQLUpdateStore, pipeline CommandPipeline, preflight StoragePreflight, generation Generation) error {
	for order, stage := range identityPipelineStages {
		fence, err := pipeline.StageFence(stage)
		if err != nil {
			return err
		}
		if err := store.StartStage(ctx, generation.ID, stage, order, fence); err != nil {
			return err
		}
		for {
			if _, err := preflight.Check(ctx); err != nil {
				return fmt.Errorf("identity evaluation stage %s storage preflight: %w", stage, err)
			}
			if _, err := pipeline.Run(ctx, stage, generation, "", ""); err != nil {
				_ = store.FailStage(context.WithoutCancel(ctx), generation, stage, safeFailure(err))
				return fmt.Errorf("identity evaluation stage %s: %w", stage, err)
			}
			completed, err := store.StageCompleted(ctx, generation.ID, stage)
			if err != nil {
				return err
			}
			if completed {
				break
			}
			if atomicSQLStages[stage] {
				return fmt.Errorf("identity evaluation stage %s omitted its terminal checkpoint", stage)
			}
		}
	}
	return nil
}

func identityRegressionWays() []int64 {
	seen := make(map[int64]struct{})
	var result []int64
	for _, regression := range defaultIdentityRegressions {
		for _, way := range regression.ways {
			if _, ok := seen[way]; ok {
				continue
			}
			seen[way] = struct{}{}
			result = append(result, way)
		}
	}
	for _, check := range defaultIdentityContextChecks {
		for _, way := range check.ways {
			if _, ok := seen[way]; ok {
				continue
			}
			seen[way] = struct{}{}
			result = append(result, way)
		}
	}
	slices.Sort(result)
	return result
}

func readIdentityEvaluation(ctx context.Context, pool *pgxpool.Pool, schema string, report *IdentityEvaluationReport) error {
	if err := pool.QueryRow(ctx, `WITH pairs AS (
		SELECT baseline.logical_path_id baseline_id,candidate.logical_path_id candidate_id
		FROM `+schema+`.baseline_identity_map baseline JOIN `+schema+`.path_segments candidate USING(segment_id)
	), splits AS (SELECT baseline_id FROM pairs GROUP BY baseline_id HAVING count(DISTINCT candidate_id)>1),
	merges AS (SELECT candidate_id FROM pairs GROUP BY candidate_id HAVING count(DISTINCT baseline_id)>1)
	SELECT count(DISTINCT baseline_id),count(DISTINCT candidate_id),(SELECT count(*) FROM splits),(SELECT count(*) FROM merges)
	FROM pairs`).Scan(&report.Identity.BaselineIdentities, &report.Identity.CandidateIdentities,
		&report.Identity.SplitIdentities, &report.Identity.MergedIdentities); err != nil {
		return fmt.Errorf("read identity evaluation summary")
	}
	if err := pool.QueryRow(ctx, `SELECT remaining_connected_splits FROM `+schema+`.attribution_identity_stats`).Scan(&report.Identity.RequiredEdgeSplits); err != nil {
		return fmt.Errorf("read identity evaluation validation")
	}
	if err := pool.QueryRow(ctx, `SELECT absorbed_segments,absorbed_length_m FROM `+schema+`.attribution_scope_sliver_stats`).
		Scan(&report.Identity.AbsorbedSlivers, &report.Identity.AbsorbedSliverM); err != nil {
		return fmt.Errorf("read identity evaluation locality slivers")
	}

	rows, err := pool.Query(ctx, `SELECT segment.logical_path_id::text,COALESCE(min(locality.name),'Outside mapped locality'),
		COALESCE(min(segment.name),'Unnamed'),CASE WHEN bool_or(segment.broad_class='road') THEN 'road' ELSE 'path' END,
		count(*),count(DISTINCT segment.source_way_id),round((sum(segment.length_m)/1000)::numeric,3)::float8
		FROM `+schema+`.path_segments segment LEFT JOIN `+schema+`.localities locality ON locality.relation_id=segment.locality_relation_id
		GROUP BY segment.logical_path_id ORDER BY count(*) DESC,segment.logical_path_id LIMIT 20`)
	if err != nil {
		return fmt.Errorf("read identity evaluation outliers")
	}
	for rows.Next() {
		var item IdentityComponentSummary
		if err := rows.Scan(&item.LogicalPathID, &item.Locality, &item.Name, &item.Class, &item.Segments, &item.SourceWays, &item.LengthKM); err != nil {
			return fmt.Errorf("scan identity evaluation outlier")
		}
		report.Outliers = append(report.Outliers, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read identity evaluation outliers")
	}
	rows.Close()

	rows, err = pool.Query(ctx, `SELECT segment.logical_path_id::text,COALESCE(min(locality.name),'Outside mapped locality'),
		'Unnamed',CASE WHEN bool_or(segment.broad_class='road') THEN 'road' ELSE 'path' END,
		count(*),count(DISTINCT segment.source_way_id),round((sum(segment.length_m)/1000)::numeric,3)::float8
		FROM `+schema+`.path_segments segment LEFT JOIN `+schema+`.localities locality ON locality.relation_id=segment.locality_relation_id
		WHERE segment.normalized_name IS NULL GROUP BY segment.logical_path_id
		ORDER BY count(*) DESC,segment.logical_path_id LIMIT 20`)
	if err != nil {
		return fmt.Errorf("read unnamed identity evaluation outliers")
	}
	for rows.Next() {
		var item IdentityComponentSummary
		if err := rows.Scan(&item.LogicalPathID, &item.Locality, &item.Name, &item.Class, &item.Segments, &item.SourceWays, &item.LengthKM); err != nil {
			rows.Close()
			return fmt.Errorf("scan unnamed identity evaluation outlier")
		}
		report.UnnamedOutliers = append(report.UnnamedOutliers, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read unnamed identity evaluation outliers")
	}
	rows.Close()

	for _, regression := range defaultIdentityRegressions {
		var identities []string
		rows, err := pool.Query(ctx, `SELECT DISTINCT segment.logical_path_id::text FROM `+schema+`.path_segments segment
			LEFT JOIN `+schema+`.localities locality ON locality.relation_id=segment.locality_relation_id
			WHERE segment.source_way_id=ANY($1)
			  AND (($2='' AND segment.locality_relation_id IS NULL) OR locality.name=$2)
			ORDER BY segment.logical_path_id::text`, regression.ways, regression.locality)
		if err != nil {
			return fmt.Errorf("read identity regression")
		}
		for rows.Next() {
			var identity string
			if err := rows.Scan(&identity); err != nil {
				rows.Close()
				return fmt.Errorf("scan identity regression")
			}
			identities = append(identities, identity)
		}
		rows.Close()
		passed := len(identities) == 1
		if regression.expectation == "different" {
			passed = len(identities) >= 2
		} else if regression.expectation == "three" {
			passed = len(identities) == 3
		}
		locality := regression.locality
		if locality == "" {
			locality = "Provider region"
		}
		report.Regressions = append(report.Regressions, IdentityRegressionResult{
			Name: regression.name, Locality: locality, Expectation: regression.expectation, SourceWayIDs: slices.Clone(regression.ways),
			IdentityCount: len(identities), Passed: passed,
		})
	}
	for _, check := range defaultIdentityContextChecks {
		var item IdentityContextResult
		item.Name, item.ExpectedLocality, item.SourceWayIDs = check.name, check.locality, slices.Clone(check.ways)
		if err := pool.QueryRow(ctx, `WITH expected AS (
			SELECT relation_id,geom FROM `+schema+`.localities WHERE name=$1 AND admin_level=8
		)
		SELECT count(*),count(*) FILTER (WHERE segment.locality_relation_id<>expected.relation_id)
		FROM `+schema+`.path_segments segment CROSS JOIN expected
		WHERE segment.source_way_id=ANY($2)
		  AND ST_Covers(expected.geom,ST_LineInterpolatePoint(segment.geom,0.5))`, check.locality, check.ways).
			Scan(&item.CoveredSegments, &item.MismatchedSegments); err != nil {
			return fmt.Errorf("read identity context regression")
		}
		item.Passed = item.CoveredSegments > 0 && item.MismatchedSegments == 0
		report.ContextChecks = append(report.ContextChecks, item)
	}
	return nil
}

type projectionSegment struct{ account, physical, current, candidate string }

func projectIdentityEvaluation(ctx context.Context, app, osmPool *pgxpool.Pool, schema string, baseline IdentityEvaluationBaseline, report *IdentityEvaluationReport) error {
	candidate := make(map[string]string, report.Identity.Segments)
	rows, err := osmPool.Query(ctx, `SELECT segment_id::text,logical_path_id::text FROM `+schema+`.path_segments`)
	if err != nil {
		return fmt.Errorf("read candidate identity map")
	}
	for rows.Next() {
		var physical, logical string
		if err := rows.Scan(&physical, &logical); err != nil {
			rows.Close()
			return fmt.Errorf("scan candidate identity map")
		}
		candidate[physical] = logical
	}
	rows.Close()

	segments := make(map[string]projectionSegment)
	accounts := make(map[string]struct{})
	currentEntities, projectedEntities := make(map[string]struct{}), make(map[string]struct{})
	oldToNew, newToOld := make(map[string]map[string]struct{}), make(map[string]map[string]struct{})
	rows, err = app.Query(ctx, `SELECT account_id::text,physical_segment_id::text,logical_path_id::text
		FROM app.path_segments WHERE region_id=$1 AND generation_id=$2`, baseline.RegionID, baseline.GenerationID)
	if err != nil {
		return fmt.Errorf("read application segment copies")
	}
	for rows.Next() {
		var account, physical, current string
		if err := rows.Scan(&account, &physical, &current); err != nil {
			rows.Close()
			return fmt.Errorf("scan application segment copy")
		}
		projected, ok := candidate[physical]
		if !ok {
			continue
		}
		key := account + "/" + physical
		segments[key] = projectionSegment{account, physical, current, projected}
		accounts[account] = struct{}{}
		currentEntities[account+"/"+current] = struct{}{}
		projectedEntities[account+"/"+projected] = struct{}{}
		addProjectionRelation(oldToNew, account+"/"+current, projected)
		addProjectionRelation(newToOld, account+"/"+projected, current)
	}
	rows.Close()

	currentVisits, projectedVisits := make(map[string]struct{}), make(map[string]struct{})
	rows, err = app.Query(ctx, `SELECT account_id::text,workout_id::text,physical_segment_id::text
		FROM app.workout_segment_matches WHERE region_id=$1 AND generation_id=$2`, baseline.RegionID, baseline.GenerationID)
	if err != nil {
		return fmt.Errorf("read application coverage matches")
	}
	for rows.Next() {
		var account, workout, physical string
		if err := rows.Scan(&account, &workout, &physical); err != nil {
			rows.Close()
			return fmt.Errorf("scan application coverage match")
		}
		segment, ok := segments[account+"/"+physical]
		if !ok {
			continue
		}
		currentVisits[account+"/"+workout+"/"+segment.current] = struct{}{}
		projectedVisits[account+"/"+workout+"/"+segment.candidate] = struct{}{}
	}
	rows.Close()

	report.Projection = &IdentityProjectionSummary{
		Accounts: len(accounts), MatchedSegments: len(segments), CurrentEntities: len(currentEntities),
		ProjectedEntities: len(projectedEntities), CurrentWorkoutAttributions: len(currentVisits),
		ProjectedWorkoutAttributions: len(projectedVisits), SplitEntities: countProjectionRelations(oldToNew),
		MergedEntities: countProjectionRelations(newToOld),
	}
	return nil
}

func addProjectionRelation(values map[string]map[string]struct{}, key, value string) {
	if values[key] == nil {
		values[key] = make(map[string]struct{})
	}
	values[key][value] = struct{}{}
}

func countProjectionRelations(values map[string]map[string]struct{}) int {
	count := 0
	for _, related := range values {
		if len(related) > 1 {
			count++
		}
	}
	return count
}
