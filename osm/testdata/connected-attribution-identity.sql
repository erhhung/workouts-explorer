-- Reference fixture for the derivation-v7 class-bounded connected identity contract.
CREATE TEMP TABLE fixture_segments (
    segment_id text PRIMARY KEY,
    logical_path_id text NOT NULL,
    park_id text,
    locality_id text,
    region_id text NOT NULL,
    normalized_name text,
    broad_class text NOT NULL,
    start_graph_node_id text NOT NULL,
    end_graph_node_id text NOT NULL
);

INSERT INTO fixture_segments VALUES
    ('E5A7797B8C532C72E23085275E2EBA82','source-a',NULL,'sunnyvale','norcal',NULL,'cycleway','node-1','unnamed-touch'),
    ('CAEF72F19BB476189D79274DC9B267EE','source-b',NULL,'sunnyvale','norcal',NULL,'footway','unnamed-touch','node-3'),
    ('named-road','road-id',NULL,'sunnyvale','norcal','stevens creek trail','road','node-4','named-touch'),
    ('named-footway','footway-id',NULL,'sunnyvale','norcal','stevens creek trail','footway','named-touch','node-6'),
    ('other-city','city-id',NULL,'cupertino','norcal','stevens creek trail','footway','named-touch','node-7'),
    ('park-contained','park-id','sleeper-park','sunnyvale','norcal','stevens creek trail','footway','park-touch','node-9'),
    ('park-outside','outside-id',NULL,'sunnyvale','norcal','stevens creek trail','footway','node-10','park-touch'),
    ('different-name','name-id',NULL,'sunnyvale','norcal','sleeper park path','footway','named-touch','node-11'),
    ('disconnected','road-id',NULL,'sunnyvale','norcal','stevens creek trail','road','node-20','node-21');

DO $fixture$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM fixture_segments a JOIN fixture_segments b
          ON a.end_graph_node_id=b.start_graph_node_id
        WHERE a.segment_id='E5A7797B8C532C72E23085275E2EBA82'
          AND b.segment_id='CAEF72F19BB476189D79274DC9B267EE'
          AND a.logical_path_id<>b.logical_path_id AND a.normalized_name IS NULL
          AND b.normalized_name IS NULL
    ) THEN RAISE EXCEPTION 'connected unnamed source IDs are not represented';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM fixture_segments a JOIN fixture_segments b
          ON a.end_graph_node_id=b.start_graph_node_id
        WHERE a.segment_id='named-road' AND b.segment_id='named-footway'
          AND a.normalized_name=b.normalized_name AND a.broad_class<>b.broad_class
    ) THEN RAISE EXCEPTION 'named broad-class boundary is not represented';
    END IF;
    IF EXISTS (
        SELECT 1 FROM fixture_segments a JOIN fixture_segments b
          ON a.end_graph_node_id IN (b.start_graph_node_id,b.end_graph_node_id)
        WHERE a.segment_id='disconnected' AND b.segment_id<>'disconnected'
    ) THEN RAISE EXCEPTION 'disconnected fixture unexpectedly touches';
    END IF;
END
$fixture$;
