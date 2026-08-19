-- Run with OSM_BUILD_SCHEMA set by psql variable after osm2pgsql completes.
CREATE INDEX IF NOT EXISTS ways_geography_gist ON :"OSM_BUILD_SCHEMA".ways
USING gist ((geom::geography));

DROP TABLE IF EXISTS :"OSM_BUILD_SCHEMA".localities;
CREATE TABLE :"OSM_BUILD_SCHEMA".localities AS
SELECT
    relation_id,
    version AS relation_version,
    NULLIF(tags->>'admin_level', '')::smallint AS admin_level,
    tags->>'name' AS name,
    lower(regexp_replace(trim(tags->>'name'), '[^[:alnum:]]+', ' ', 'g')) AS normalized_name,
    tags,
    ST_Multi(geom)::geometry(MultiPolygon, 4326) AS geom
FROM :"OSM_BUILD_SCHEMA".boundaries
WHERE geom IS NOT NULL
  AND tags->>'boundary' = 'administrative'
  AND tags->>'admin_level' = '8'
  AND ST_IsValid(geom)
  AND NOT ST_IsEmpty(geom);

ALTER TABLE :"OSM_BUILD_SCHEMA".localities ADD PRIMARY KEY (relation_id);
CREATE INDEX localities_geom_gist ON :"OSM_BUILD_SCHEMA".localities USING gist (geom);
ANALYZE :"OSM_BUILD_SCHEMA".ways;
ANALYZE :"OSM_BUILD_SCHEMA".localities;
