-- Importer v4 adds public residential context without changing the v3 road,
-- park, education or boundary extraction used by retained generation checkpoints.
local root = assert(os.getenv('OSM_IMPORT_ROOT'), 'OSM_IMPORT_ROOT is required')
dofile(root .. '/import.lua')
local schema = os.getenv('OSM_BUILD_SCHEMA')

local building_ways = osm2pgsql.define_way_table('residential_building_ways', {
    { column = 'version', type = 'int' },
    { column = 'tags', type = 'jsonb' },
    { column = 'geom', type = 'polygon', projection = 4326 },
}, { schema = schema })
local building_relations = osm2pgsql.define_relation_table('residential_building_relations', {
    { column = 'version', type = 'int' },
    { column = 'tags', type = 'jsonb' },
    { column = 'members', type = 'jsonb' },
    { column = 'geom', type = 'multipolygon', projection = 4326 },
}, { schema = schema })
local landuse_ways = osm2pgsql.define_way_table('residential_landuse_ways', {
    { column = 'geom', type = 'polygon', projection = 4326 },
}, { schema = schema })
local landuse_relations = osm2pgsql.define_relation_table('residential_landuse_relations', {
    { column = 'geom', type = 'multipolygon', projection = 4326 },
}, { schema = schema })
local addresses = osm2pgsql.define_node_table('residential_address_nodes', {
    { column = 'tags', type = 'jsonb' },
    { column = 'geom', type = 'point', projection = 4326 },
}, { schema = schema })

local original_way = osm2pgsql.process_way
function osm2pgsql.process_way(object)
    original_way(object)
    if object.is_closed and object.tags.building ~= nil and object.tags.building ~= 'no'
        and object.tags['building:part'] == nil then
        building_ways:insert({version = object.version, tags = object.tags, geom = object:as_polygon()})
    end
    if object.is_closed and object.tags.landuse == 'residential' then
        landuse_ways:insert({geom = object:as_polygon()})
    end
end

local original_relation = osm2pgsql.process_relation
function osm2pgsql.process_relation(object)
    original_relation(object)
    if object.tags.building ~= nil and object.tags.building ~= 'no' and object.tags['building:part'] == nil then
        building_relations:insert({version = object.version, tags = object.tags,
            members = object.members, geom = object:as_multipolygon()})
    end
    if object.tags.landuse == 'residential' then
        landuse_relations:insert({geom = object:as_multipolygon()})
    end
end

function osm2pgsql.process_node(object)
    if object.tags['addr:housenumber'] ~= nil or object.tags.entrance ~= nil then
        addresses:insert({tags = object.tags, geom = object:as_point()})
    end
end
