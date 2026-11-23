package database

const runtimeOSMCatalogReadinessSQL = `
    to_regclass('app.osm_region_updates') IS NOT NULL
    AND to_regclass('app.osm_region_update_steps') IS NOT NULL
    AND to_regprocedure('app.osm_update_event(uuid,text,jsonb)') IS NOT NULL
    AND EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid='app.osm_region_updates'::regclass AND attname='elapsed_total_ms' AND NOT attisdropped)
    AND has_function_privilege('workouts_osm_executor','app.osm_update_event(uuid,text,jsonb)','EXECUTE')
    AND NOT has_table_privilege('workouts_osm_executor','app.osm_region_updates','SELECT,UPDATE')
    AND
    to_regclass('app.osm_catalog_refresh_jobs') IS NOT NULL
    AND to_regclass('app.osm_region_catalog') IS NOT NULL
    AND to_regclass('app.osm_region_workouts') IS NOT NULL
    AND to_regprocedure('app.admin_osm_region_workout_counts(uuid)') IS NOT NULL
    AND has_function_privilege('workouts_api','app.admin_osm_region_workout_counts(uuid)','EXECUTE')
    AND NOT has_table_privilege('workouts_api','app.osm_region_workouts','SELECT')
    AND to_regclass('app.osm_provider_catalogs') IS NOT NULL
    AND to_regclass('app.osm_catalog_refresh_one_active') IS NOT NULL
    AND has_table_privilege('workouts_api','app.osm_catalog_refresh_jobs','SELECT')
    AND NOT has_table_privilege('workouts_api','app.osm_catalog_refresh_jobs','UPDATE')
    AND has_column_privilege('workouts_api','app.osm_catalog_refresh_jobs','id','INSERT')
    AND has_table_privilege('workouts_worker','app.osm_catalog_refresh_jobs','UPDATE')
    AND has_table_privilege('workouts_worker','app.osm_region_catalog','INSERT,UPDATE,SELECT')`
