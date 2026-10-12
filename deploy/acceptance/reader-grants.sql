-- Scratch OSM databases only. Migration-owner privileges remain unchanged.
BEGIN;
GRANT USAGE ON SCHEMA osm_catalog,osm_canonical,osm_active TO workouts_coverage_worker;
GRANT SELECT ON ALL TABLES IN SCHEMA osm_catalog,osm_canonical,osm_active TO workouts_coverage_worker;
REVOKE EXECUTE ON ALL FUNCTIONS IN SCHEMA osm_catalog,osm_active FROM PUBLIC,workouts_coverage_worker;
GRANT EXECUTE ON FUNCTION osm_catalog.promotion_event_head(),
  osm_catalog.read_promotion_events(bigint,integer),
  osm_active.timezone_at(double precision,double precision) TO workouts_coverage_worker;
DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
    WHERE n.nspname IN ('osm_catalog','osm_active')
      AND p.proname NOT IN ('promotion_event_head','read_promotion_events','timezone_at')
      AND has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')) THEN
    RAISE EXCEPTION 'scratch reader can execute a mutable OSM function';
  END IF;
END $$;
COMMIT;
