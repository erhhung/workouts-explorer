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

function osm2pgsql.process_way(object)
    if object.tags.highway == nil then
        return
    end
    ways:insert({
        version = object.version,
        osm_timestamp = object.timestamp,
        tags = object.tags,
        node_ids = object.nodes,
        geom = object:as_linestring(),
    })
end

function osm2pgsql.process_relation(object)
    if object.tags.boundary ~= 'administrative' then
        return
    end
    boundaries:insert({
        version = object.version,
        osm_timestamp = object.timestamp,
        tags = object.tags,
        members = object.members,
        geom = object:as_multipolygon(),
    })
end
