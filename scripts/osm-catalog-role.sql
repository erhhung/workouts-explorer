-- Run on the OSM cluster before migration 011. Supply the runtime password
-- separately through the installation's Secret management, never this file.
DO $role$
BEGIN
    IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='workouts_osm_catalog') THEN
        CREATE ROLE workouts_osm_catalog LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
    END IF;
    IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='workouts_osm_catalog'
        AND rolcanlogin AND NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole AND NOT rolreplication AND NOT rolbypassrls)
        OR EXISTS(SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON r.oid=m.member WHERE r.rolname='workouts_osm_catalog') THEN
        RAISE EXCEPTION 'unsafe OSM catalog runtime role';
    END IF;
END;
$role$;
ALTER ROLE workouts_osm_catalog SET statement_timeout='30s';
ALTER ROLE workouts_osm_catalog SET lock_timeout='5s';
ALTER ROLE workouts_osm_catalog SET idle_in_transaction_session_timeout='30s';
