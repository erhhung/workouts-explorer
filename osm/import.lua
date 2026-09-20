local schema = os.getenv('OSM_BUILD_SCHEMA')

if schema == nil or not string.match(schema, '^osm_build_[0-9]+$') then
    error('OSM_BUILD_SCHEMA must be a generation schema name')
end

local ways = osm2pgsql.define_way_table('ways', {
    { column = 'version', type = 'int' },
    { column = 'osm_timestamp', type = 'text' },
    { column = 'tags', type = 'jsonb' },
    { column = 'node_ids', type = 'jsonb' },
    { column = 'geom', type = 'linestring', projection = 4326, not_null = true },
}, { schema = schema })

local boundaries = osm2pgsql.define_relation_table('boundaries', {
    { column = 'version', type = 'int' },
    { column = 'osm_timestamp', type = 'text' },
    { column = 'tags', type = 'jsonb' },
    { column = 'members', type = 'jsonb' },
    { column = 'geom', type = 'multipolygon', projection = 4326 },
}, { schema = schema })

local park_ways = osm2pgsql.define_way_table('park_ways', {
    { column = 'version', type = 'int' },
    { column = 'tags', type = 'jsonb' },
    { column = 'geom', type = 'polygon', projection = 4326 },
}, { schema = schema })

local park_relations = osm2pgsql.define_relation_table('park_relations', {
    { column = 'version', type = 'int' },
    { column = 'tags', type = 'jsonb' },
    { column = 'geom', type = 'multipolygon', projection = 4326 },
}, { schema = schema })

local education_ways = osm2pgsql.define_way_table('education_ways', {
    { column = 'version', type = 'int' },
    { column = 'tags', type = 'jsonb' },
    { column = 'geom', type = 'polygon', projection = 4326 },
}, { schema = schema })

local education_relations = osm2pgsql.define_relation_table('education_relations', {
    { column = 'version', type = 'int' },
    { column = 'tags', type = 'jsonb' },
    { column = 'geom', type = 'multipolygon', projection = 4326 },
}, { schema = schema })

local function park_candidate(tags)
    local protection_title = tags.protection_title
    local national_park = tags.boundary == 'national_park' or
        tags.protected_area == 'national_park' or
        (protection_title ~= nil and string.lower(protection_title):match('^%s*national%s+park%s*$') ~= nil)
    return tags.leisure == 'park' or tags.leisure == 'nature_reserve' or
        tags.boundary == 'protected_area' or national_park
end

local function education_candidate(tags)
    return tags.amenity == 'school' or tags.amenity == 'college' or
        tags.amenity == 'university' or tags.landuse == 'education'
end

function osm2pgsql.process_way(object)
	if object.tags.highway ~= nil then
		ways:insert({
			version = object.version,
			osm_timestamp = object.timestamp,
			tags = object.tags,
			node_ids = object.nodes,
			geom = object:as_linestring(),
		})
	end
	if object.is_closed and park_candidate(object.tags) then
		park_ways:insert({version = object.version, tags = object.tags, geom = object:as_polygon()})
	end
	if object.is_closed and education_candidate(object.tags) then
		education_ways:insert({version = object.version, tags = object.tags, geom = object:as_polygon()})
	end
end

function osm2pgsql.process_relation(object)
	if object.tags.boundary == 'administrative' then
		boundaries:insert({
			version = object.version,
			osm_timestamp = object.timestamp,
			tags = object.tags,
			members = object.members,
			geom = object:as_multipolygon(),
		})
	end
	if park_candidate(object.tags) then
		park_relations:insert({version = object.version, tags = object.tags, geom = object:as_multipolygon()})
	end
	if education_candidate(object.tags) then
		education_relations:insert({version = object.version, tags = object.tags, geom = object:as_multipolygon()})
	end
end
