SET search_path TO :"OSM_BUILD_SCHEMA", public;

CREATE TABLE IF NOT EXISTS attribution_identity_named_roads (
    group_id uuid NOT NULL,
    segment_id uuid PRIMARY KEY,
    source_way_id bigint NOT NULL,
    broad_class text NOT NULL,
    oneway boolean NOT NULL,
    geom geometry(LineString, 4326) NOT NULL
);

CREATE TABLE IF NOT EXISTS attribution_identity_proximity_winners (
    winner_id uuid PRIMARY KEY,
    group_id uuid NOT NULL,
    left_source_way_id bigint NOT NULL,
    right_source_way_id bigint NOT NULL,
    left_segment_id uuid NOT NULL,
    right_segment_id uuid NOT NULL,
    left_broad_class text NOT NULL,
    right_broad_class text NOT NULL,
    distance_m double precision NOT NULL,
    UNIQUE (group_id, left_source_way_id, right_source_way_id)
);

CREATE OR REPLACE PROCEDURE stage_identity_proximity(target_generation bigint)
LANGUAGE plpgsql
AS $procedure$
DECLARE
    phase text;
    cursor_id uuid;
    next_cursor uuid;
    expected_batch bigint;
    processed bigint;
    batch_rows bigint;
    batch_index bigint;
    batch_total bigint;
    phase_rows bigint;
    cursor_rows bigint;
    batch_size constant integer := 500000;
BEGIN
    SELECT coalesce(cursor->>'phase', 'roads'),
        NULLIF(cursor->>'last_id', '')::uuid,
        batch_count,
        rows_processed,
        (cursor->>'batch_index')::bigint,
        (cursor->>'batch_total')::bigint
    INTO phase, cursor_id, expected_batch, processed, batch_index, batch_total
    FROM osm_catalog.generation_stages
    WHERE generation_id = target_generation AND stage = 'identity-proximity'
    FOR UPDATE;

    IF batch_index IS NULL OR batch_total IS NULL THEN
        IF phase = 'roads' THEN
            SELECT count(*), count(*) FILTER (WHERE cursor_id IS NOT NULL AND segment_id <= cursor_id)
            INTO phase_rows, cursor_rows FROM attribution_identity_segments;
        ELSIF phase = 'candidates' THEN
            SELECT count(*), count(*) FILTER (WHERE cursor_id IS NOT NULL AND segment_id <= cursor_id)
            INTO phase_rows, cursor_rows FROM attribution_identity_named_roads;
        ELSIF phase = 'edges' THEN
            SELECT count(*), count(*) FILTER (WHERE cursor_id IS NOT NULL AND winner_id <= cursor_id)
            INTO phase_rows, cursor_rows FROM attribution_identity_proximity_winners;
        END IF;
        batch_index := (cursor_rows + batch_size - 1) / batch_size + 1;
        batch_total := (phase_rows + batch_size - 1) / batch_size + 1;
    END IF;

    IF phase = 'roads' THEN
    CREATE TEMP TABLE proximity_keys ON COMMIT DROP AS SELECT segment_id FROM attribution_identity_segments
      WHERE cursor_id IS NULL OR segment_id>cursor_id ORDER BY segment_id LIMIT batch_size;
    GET DIAGNOSTICS batch_rows=ROW_COUNT;
    IF batch_rows=0 THEN
      CREATE INDEX IF NOT EXISTS attribution_identity_named_roads_group_idx ON attribution_identity_named_roads(group_id,segment_id);
      CREATE INDEX IF NOT EXISTS attribution_identity_named_roads_geom_gist ON attribution_identity_named_roads USING gist(geom);
      ANALYZE attribution_identity_named_roads;
      SELECT count(*) INTO phase_rows FROM attribution_identity_named_roads;
      PERFORM osm_catalog.checkpoint_generation_stage(target_generation,'identity-proximity',expected_batch,
        jsonb_build_object('version',1,'phase','candidates','last_id',NULL,'batch_size',batch_size,
          'batch_index',1,'batch_total',(phase_rows+batch_size-1)/batch_size+1),processed);
      RETURN;
    END IF;
        SELECT segment_id INTO next_cursor FROM proximity_keys ORDER BY segment_id DESC LIMIT 1;
    INSERT INTO attribution_identity_named_roads
    SELECT identity.group_id,identity.segment_id,identity.source_way_id,identity.broad_class,identity.oneway,segment.geom
    FROM proximity_keys key JOIN attribution_identity_segments identity USING(segment_id) JOIN path_segments segment USING(segment_id)
    WHERE identity.broad_class='road' AND NOT identity.unnamed ON CONFLICT(segment_id) DO NOTHING;
    ELSIF phase = 'candidates' THEN
    CREATE TEMP TABLE proximity_keys ON COMMIT DROP AS SELECT segment_id FROM attribution_identity_named_roads
      WHERE cursor_id IS NULL OR segment_id>cursor_id ORDER BY segment_id LIMIT batch_size;
    GET DIAGNOSTICS batch_rows=ROW_COUNT;
    IF batch_rows=0 THEN
      SELECT count(*) INTO phase_rows FROM attribution_identity_proximity_winners;
      PERFORM osm_catalog.checkpoint_generation_stage(target_generation,'identity-proximity',expected_batch,
        jsonb_build_object('version',1,'phase','edges','last_id',NULL,'batch_size',batch_size,
          'batch_index',1,'batch_total',(phase_rows+batch_size-1)/batch_size+1),processed);
      RETURN;
    END IF;
        SELECT segment_id INTO next_cursor FROM proximity_keys ORDER BY segment_id DESC LIMIT 1;
    INSERT INTO attribution_identity_proximity_winners
    SELECT md5(format('workouts-explorer/osm-proximity-pair/v1:%s:%s:%s',nearby.group_id,
      least(nearby.left_source_way_id,nearby.right_source_way_id),greatest(nearby.left_source_way_id,nearby.right_source_way_id)))::uuid,
      nearby.group_id,least(nearby.left_source_way_id,nearby.right_source_way_id),greatest(nearby.left_source_way_id,nearby.right_source_way_id),
      nearby.left_segment_id,nearby.right_segment_id,nearby.left_broad_class,nearby.right_broad_class,nearby.distance_m
    FROM (
      SELECT DISTINCT ON(a.group_id,least(a.source_way_id,candidate.source_way_id),greatest(a.source_way_id,candidate.source_way_id))
        a.group_id,a.segment_id left_segment_id,candidate.segment_id right_segment_id,a.source_way_id left_source_way_id,
        candidate.source_way_id right_source_way_id,a.broad_class left_broad_class,candidate.broad_class right_broad_class,
        ST_Distance(a.geom::geography,candidate.geom::geography) distance_m
      FROM proximity_keys key JOIN attribution_identity_named_roads a USING(segment_id)
      JOIN LATERAL(SELECT candidate.* FROM attribution_identity_named_roads candidate WHERE candidate.group_id=a.group_id
        AND candidate.segment_id>a.segment_id AND candidate.geom&&ST_Expand(a.geom,0.0006)
        AND ST_DWithin(a.geom::geography,candidate.geom::geography,50)
        AND (ST_DWithin(a.geom::geography,candidate.geom::geography,15) OR (a.oneway AND candidate.oneway))) candidate ON true
      ORDER BY a.group_id,least(a.source_way_id,candidate.source_way_id),greatest(a.source_way_id,candidate.source_way_id),
        distance_m,a.segment_id,candidate.segment_id
    ) nearby
    ON CONFLICT (group_id, left_source_way_id, right_source_way_id) DO UPDATE SET
      left_segment_id = excluded.left_segment_id, right_segment_id = excluded.right_segment_id,
      left_broad_class = excluded.left_broad_class, right_broad_class = excluded.right_broad_class,
      distance_m = excluded.distance_m
    WHERE (excluded.distance_m, excluded.left_segment_id, excluded.right_segment_id) <
      (attribution_identity_proximity_winners.distance_m, attribution_identity_proximity_winners.left_segment_id,
       attribution_identity_proximity_winners.right_segment_id);
    ELSIF phase = 'edges' THEN
    CREATE TEMP TABLE proximity_keys ON COMMIT DROP AS SELECT winner_id FROM attribution_identity_proximity_winners
      WHERE cursor_id IS NULL OR winner_id>cursor_id ORDER BY winner_id LIMIT batch_size;
    GET DIAGNOSTICS batch_rows=ROW_COUNT;
    IF batch_rows=0 THEN
      DROP TABLE attribution_identity_proximity_winners;
      PERFORM osm_catalog.complete_generation_stage(target_generation,'identity-proximity',expected_batch,
        jsonb_build_object('version',1,'phase','done','last_id',cursor_id,'batch_size',batch_size,
          'batch_index',batch_index,'batch_total',batch_total),processed);
      RETURN;
    END IF;
        SELECT winner_id INTO next_cursor FROM proximity_keys ORDER BY winner_id DESC LIMIT 1;
    INSERT INTO attribution_identity_edges
    SELECT winner.group_id,md5(format('workouts-explorer/osm-named-road-proximity/v1:%s:%s',winner.left_segment_id,winner.right_segment_id))::uuid,
      least(winner.left_segment_id,winner.right_segment_id),greatest(winner.left_segment_id,winner.right_segment_id),
      CASE WHEN winner.left_segment_id<winner.right_segment_id THEN winner.left_source_way_id ELSE winner.right_source_way_id END,
      CASE WHEN winner.left_segment_id<winner.right_segment_id THEN winner.right_source_way_id ELSE winner.left_source_way_id END,false,
      CASE WHEN winner.left_segment_id<winner.right_segment_id THEN winner.left_broad_class ELSE winner.right_broad_class END,
      CASE WHEN winner.left_segment_id<winner.right_segment_id THEN winner.right_broad_class ELSE winner.left_broad_class END
    FROM proximity_keys key JOIN attribution_identity_proximity_winners winner USING(winner_id) ON CONFLICT DO NOTHING;
    ELSE
        RAISE EXCEPTION 'unknown identity proximity phase %', phase;
    END IF;
  PERFORM osm_catalog.checkpoint_generation_stage(target_generation,'identity-proximity',expected_batch,
    jsonb_build_object('version',1,'phase',phase,'last_id',next_cursor,'batch_size',batch_size,
      'batch_index',batch_index+1,'batch_total',batch_total),processed+batch_rows);
END;
$procedure$;

CALL stage_identity_proximity(:'OSM_GENERATION_ID'::bigint);

DROP PROCEDURE stage_identity_proximity(bigint);
