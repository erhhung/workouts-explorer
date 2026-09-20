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
  AND tags->>'admin_level' IN ('6','8')
  AND ST_IsValid(geom)
  AND NOT ST_IsEmpty(geom);

ALTER TABLE :"OSM_BUILD_SCHEMA".localities ADD PRIMARY KEY (relation_id);
CREATE INDEX localities_geom_gist ON :"OSM_BUILD_SCHEMA".localities USING gist (geom);

DROP TABLE IF EXISTS :"OSM_BUILD_SCHEMA".park_areas;
CREATE TABLE :"OSM_BUILD_SCHEMA".park_areas AS
WITH candidates AS (
    SELECT 'way'::text AS source_type,way_id AS source_id,version,tags,ST_Multi(geom)::geometry(MultiPolygon,4326) AS geom
    FROM :"OSM_BUILD_SCHEMA".park_ways WHERE geom IS NOT NULL
    UNION ALL
    SELECT 'relation',relation_id,version,tags,ST_Multi(geom)::geometry(MultiPolygon,4326)
    FROM :"OSM_BUILD_SCHEMA".park_relations WHERE geom IS NOT NULL
), classified AS (
    SELECT candidates.*,
        CASE
            WHEN lower(tags->>'boundary')='national_park'
              OR lower(tags->>'protected_area')='national_park'
              OR lower(btrim(tags->>'protection_title'))='national park' THEN 'national_park'
            WHEN tags->>'leisure'='park' THEN 'local_park'
            WHEN tags->>'leisure'='nature_reserve' THEN 'nature_reserve'
            WHEN tags->>'boundary'='protected_area' THEN 'protected_area'
        END AS park_kind
    FROM candidates
), measured AS (
    SELECT classified.*,NULLIF(btrim(tags->>'name'),'') AS name,
        NULLIF(lower(regexp_replace(btrim(tags->>'name'),'[[:space:]]+',' ','g')),'') AS normalized_name,
        ST_Area(geom::geography) AS area_m2,
        CASE park_kind WHEN 'local_park' THEN 1 WHEN 'nature_reserve' THEN 2
             WHEN 'protected_area' THEN 3 WHEN 'national_park' THEN 4 END AS type_priority
    FROM classified
    WHERE ST_IsValid(geom) AND NOT ST_IsEmpty(geom)
      AND park_kind IS NOT NULL
      AND (park_kind='national_park' OR tags->>'protect_class' IS DISTINCT FROM '2')
)
SELECT source_type,source_id,version,name,normalized_name,park_kind,tags,geom,area_m2,type_priority
FROM measured WHERE name IS NOT NULL AND (
    (park_kind='local_park' AND area_m2 BETWEEN 500 AND 25000000) OR
    (park_kind='nature_reserve' AND area_m2 BETWEEN 1000 AND 10000000) OR
    (park_kind='protected_area' AND area_m2 BETWEEN 1000 AND 10000000) OR
    (park_kind='national_park' AND area_m2 BETWEEN 1000000 AND 100000000000)
);
ALTER TABLE :"OSM_BUILD_SCHEMA".park_areas ADD PRIMARY KEY(source_type,source_id);
CREATE INDEX park_areas_geom_gist ON :"OSM_BUILD_SCHEMA".park_areas USING gist(geom);

DROP TABLE IF EXISTS :"OSM_BUILD_SCHEMA".education_areas;
CREATE TABLE :"OSM_BUILD_SCHEMA".education_areas AS
WITH candidates AS (
    SELECT 'way'::text source_type,way_id source_id,version,tags,ST_Multi(geom)::geometry(MultiPolygon,4326) geom
    FROM :"OSM_BUILD_SCHEMA".education_ways WHERE geom IS NOT NULL
    UNION ALL
    SELECT 'relation',relation_id,version,tags,ST_Multi(geom)::geometry(MultiPolygon,4326)
    FROM :"OSM_BUILD_SCHEMA".education_relations WHERE geom IS NOT NULL
), measured AS (
    SELECT candidates.*,NULLIF(btrim(tags->>'name'),'') name,
        NULLIF(lower(regexp_replace(btrim(tags->>'name'),'[[:space:]]+',' ','g')),'') normalized_name,
        CASE WHEN tags->>'amenity' IN ('school','college','university') THEN tags->>'amenity'
             WHEN tags->>'landuse'='education' THEN 'education' END education_kind,
        ST_Area(geom::geography) area_m2
    FROM candidates WHERE ST_IsValid(geom) AND NOT ST_IsEmpty(geom)
)
SELECT source_type,source_id,version,name,normalized_name,education_kind,tags,geom,area_m2,
    CASE education_kind WHEN 'school' THEN 1 WHEN 'college' THEN 2 WHEN 'university' THEN 3 ELSE 4 END type_priority
FROM measured WHERE name IS NOT NULL AND education_kind IS NOT NULL AND (
    (education_kind='school' AND area_m2 BETWEEN 1000 AND 20000000) OR
    (education_kind='college' AND area_m2 BETWEEN 1000 AND 100000000) OR
    (education_kind='university' AND area_m2 BETWEEN 5000 AND 500000000) OR
    (education_kind='education' AND area_m2 BETWEEN 1000 AND 500000000)
);
ALTER TABLE :"OSM_BUILD_SCHEMA".education_areas ADD PRIMARY KEY(source_type,source_id);
CREATE INDEX education_areas_geom_gist ON :"OSM_BUILD_SCHEMA".education_areas USING gist(geom);
ANALYZE :"OSM_BUILD_SCHEMA".ways;
ANALYZE :"OSM_BUILD_SCHEMA".localities;
ANALYZE :"OSM_BUILD_SCHEMA".park_areas;
ANALYZE :"OSM_BUILD_SCHEMA".education_areas;
