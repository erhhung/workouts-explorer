package osm

import (
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type attributionEdge struct {
	segmentID, logicalID, sourceWay, educationID, parkID, regionID string
	localityID, name, normalizedName                               string
	broadClass, startNode, endNode                                 string
}

func TestAttributionUsesStorageBoundedSegmentRewrite(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "osm", "identity-rewrite-batched.sql"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS path_segments_rewritten",
		"INSERT INTO path_segments_rewritten",
		"DROP TABLE path_segments;",
		"ALTER TABLE path_segments_rewritten RENAME TO path_segments;",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("identity rewrite script missing %q", required)
		}
	}
	if strings.Contains(text, "UPDATE path_segments segment\nSET logical_path_id=component.merged_logical_path_id") {
		t.Error("attribute script retains the storage-amplifying mass update")
	}
}

func referenceAttributionPathIDs(edges []attributionEdge) map[string]string {
	type group struct{ scopeKind, scopeID, nameKey, broadClass string }
	type member struct {
		group
		segmentID string
	}
	keyFor := func(edge attributionEdge) group {
		attributionClass := "path"
		if edge.broadClass == "road" {
			attributionClass = "road"
		}
		key := group{nameKey: "U", scopeKind: "region", scopeID: edge.regionID, broadClass: attributionClass}
		if edge.normalizedName != "" {
			identityName := edge.normalizedName
			if edge.name != "" {
				identityName = strings.ToLower(strings.Join(strings.Fields(edge.name), " "))
			}
			key.nameKey = "N:" + identityName
			key.broadClass = "named"
		}
		if edge.localityID != "" {
			key.scopeKind, key.scopeID = "locality", edge.localityID
		}
		if edge.normalizedName == "" && edge.educationID != "" {
			key.scopeKind, key.scopeID = "education", edge.educationID
		} else if edge.parkID != "" {
			key.scopeKind, key.scopeID = "park", edge.parkID
		}
		return key
	}
	connected := func(a, b attributionEdge) bool {
		return a.startNode == b.startNode || a.startNode == b.endNode ||
			a.endNode == b.startNode || a.endNode == b.endNode
	}
	md5UUID := func(identity string) string {
		digest := fmt.Sprintf("%x", md5.Sum([]byte(identity)))
		return digest[:8] + "-" + digest[8:12] + "-" + digest[12:16] + "-" + digest[16:20] + "-" + digest[20:]
	}
	groupID := func(key group) string {
		identity := fmt.Sprintf("workouts-explorer/osm-attribution-group/v3:%d:%s:%d:%s:name:%d:%s:class:%d:%s",
			len(key.scopeKind), key.scopeKind, len(key.scopeID), key.scopeID,
			len(key.nameKey), key.nameKey, len(key.broadClass), key.broadClass)
		return md5UUID(identity)
	}
	mergedID := func(key group, memberID string) string {
		return md5UUID(fmt.Sprintf("workouts-explorer/osm-logical-path/v12:group:%s:member:%s",
			groupID(key), memberID))
	}
	type groupNode struct {
		group
		node string
	}
	type groupNodeClass struct {
		group
		node, broadClass string
	}
	incidence := make(map[groupNode]map[string]struct{})
	classIncidence := make(map[groupNodeClass]map[string]struct{})
	for _, edge := range edges {
		key := keyFor(edge)
		for _, node := range []string{edge.startNode, edge.endNode} {
			item := groupNode{key, node}
			if incidence[item] == nil {
				incidence[item] = make(map[string]struct{})
			}
			incidence[item][edge.segmentID] = struct{}{}
			classItem := groupNodeClass{key, node, edge.broadClass}
			if classIncidence[classItem] == nil {
				classIncidence[classItem] = make(map[string]struct{})
			}
			classIncidence[classItem][edge.segmentID] = struct{}{}
		}
	}
	requiredConnection := func(key group, a, b attributionEdge) bool {
		if a.normalizedName != "" {
			return true
		}
		if a.sourceWay != "" && a.sourceWay == b.sourceWay {
			return true
		}
		for _, node := range []string{a.startNode, a.endNode} {
			if node == b.startNode || node == b.endNode {
				if len(incidence[groupNode{key, node}]) == 2 {
					return true
				}
				if a.broadClass == b.broadClass && len(classIncidence[groupNodeClass{key, node, a.broadClass}]) == 2 {
					return true
				}
			}
		}
		return false
	}

	parent := make(map[member]member)
	find := func(item member) member { return item }
	find = func(item member) member {
		root := item
		for parent[root] != root {
			root = parent[root]
		}
		for parent[item] != item {
			next := parent[item]
			parent[item] = root
			item = next
		}
		return root
	}
	for i, a := range edges {
		left := member{keyFor(a), a.segmentID}
		if _, ok := parent[left]; !ok {
			parent[left] = left
		}
		for _, b := range edges[i+1:] {
			key := keyFor(a)
			if key != keyFor(b) || !connected(a, b) || !requiredConnection(key, a, b) {
				continue
			}
			left, right := member{key, a.segmentID}, member{key, b.segmentID}
			if _, ok := parent[right]; !ok {
				parent[right] = right
			}
			left, right = find(left), find(right)
			if left.segmentID < right.segmentID {
				parent[right] = left
			} else if right.segmentID < left.segmentID {
				parent[left] = right
			}
		}
	}

	ids := make(map[string]string, len(edges))
	for _, edge := range edges {
		key := keyFor(edge)
		root := find(member{key, edge.segmentID})
		ids[edge.segmentID] = mergedID(key, root.segmentID)
	}
	return ids
}

func TestUnnamedAttributionKeepsOneExactClassPairAtABranch(t *testing.T) {
	edges := []attributionEdge{
		{segmentID: "cycleway-one", sourceWay: "one", localityID: "cupertino", regionID: "norcal", broadClass: "cycleway", startNode: "a", endNode: "branch"},
		{segmentID: "cycleway-two", sourceWay: "two", localityID: "cupertino", regionID: "norcal", broadClass: "cycleway", startNode: "branch", endNode: "b"},
		{segmentID: "footway-spur", sourceWay: "spur", localityID: "cupertino", regionID: "norcal", broadClass: "footway", startNode: "branch", endNode: "c"},
	}
	ids := referenceAttributionPathIDs(edges)
	if ids["cycleway-one"] != ids["cycleway-two"] {
		t.Fatalf("the only exact-class pair was split at a branch: %v", ids)
	}
	if ids["cycleway-one"] == ids["footway-spur"] {
		t.Fatalf("a different-class spur merged at a branch: %v", ids)
	}
}

func TestNamedAttributionMergesAcrossCrossWayBranches(t *testing.T) {
	edges := []attributionEdge{
		{segmentID: "meteor-one", sourceWay: "one", localityID: "cupertino", regionID: "norcal", normalizedName: "meteor drive", broadClass: "road", startNode: "a", endNode: "branch"},
		{segmentID: "meteor-two", sourceWay: "two", localityID: "cupertino", regionID: "norcal", normalizedName: "meteor drive", broadClass: "road", startNode: "branch", endNode: "b"},
		{segmentID: "meteor-three", sourceWay: "three", localityID: "cupertino", regionID: "norcal", normalizedName: "meteor drive", broadClass: "road", startNode: "branch", endNode: "c"},
	}
	ids := referenceAttributionPathIDs(edges)
	if ids["meteor-one"] != ids["meteor-two"] || ids["meteor-one"] != ids["meteor-three"] {
		t.Fatalf("named road was split at a cross-way branch: %v", ids)
	}
}

func TestNamedAttributionPreservesDirectionalRoadLabels(t *testing.T) {
	edges := []attributionEdge{
		{segmentID: "east-one", name: "East Homestead Road", normalizedName: "homestead road", localityID: "cupertino", regionID: "norcal", broadClass: "road", startNode: "a", endNode: "shared"},
		{segmentID: "east-two", name: " east   HOMESTEAD road ", normalizedName: "homestead road", localityID: "cupertino", regionID: "norcal", broadClass: "road", startNode: "shared", endNode: "b"},
		{segmentID: "west", name: "West Homestead Road", normalizedName: "homestead road", localityID: "cupertino", regionID: "norcal", broadClass: "road", startNode: "shared", endNode: "c"},
		{segmentID: "plain", name: "Homestead Road", normalizedName: "homestead road", localityID: "cupertino", regionID: "norcal", broadClass: "road", startNode: "shared", endNode: "d"},
	}
	ids := referenceAttributionPathIDs(edges)
	if ids["east-one"] != ids["east-two"] {
		t.Fatalf("case/whitespace-equivalent East labels split: %v", ids)
	}
	if ids["east-one"] == ids["west"] || ids["east-one"] == ids["plain"] || ids["west"] == ids["plain"] {
		t.Fatalf("directional and plain labels merged at a shared node: %v", ids)
	}
}

func TestEducationScopeAppliesOnlyToUnnamedSegments(t *testing.T) {
	edges := []attributionEdge{
		{segmentID: "school-one", sourceWay: "one", educationID: "school", localityID: "city", regionID: "norcal", broadClass: "footway", startNode: "a", endNode: "touch"},
		{segmentID: "school-two", sourceWay: "two", educationID: "school", localityID: "city", regionID: "norcal", broadClass: "footway", startNode: "touch", endNode: "b"},
		{segmentID: "outside", sourceWay: "three", localityID: "city", regionID: "norcal", broadClass: "footway", startNode: "touch", endNode: "c"},
	}
	ids := referenceAttributionPathIDs(edges)
	if ids["school-one"] != ids["school-two"] || ids["school-one"] == ids["outside"] {
		t.Fatalf("unnamed education scope is incorrect: %v", ids)
	}
	edges[0].normalizedName, edges[2].normalizedName = "campus drive", "campus drive"
	ids = referenceAttributionPathIDs(edges)
	if ids["school-one"] != ids["outside"] {
		t.Fatalf("named segment did not ignore education scope: %v", ids)
	}
}

func TestAttributionStopsCrossWayMergesAtBranches(t *testing.T) {
	edges := []attributionEdge{
		{segmentID: "through-one", sourceWay: "main", localityID: "city", regionID: "norcal", startNode: "a", endNode: "branch"},
		{segmentID: "through-two", sourceWay: "main", localityID: "city", regionID: "norcal", startNode: "branch", endNode: "b"},
		{segmentID: "spur", sourceWay: "spur", localityID: "city", regionID: "norcal", startNode: "branch", endNode: "c"},
	}
	ids := referenceAttributionPathIDs(edges)
	if ids["through-one"] != ids["through-two"] {
		t.Fatalf("one source way was split at a branch: %v", ids)
	}
	if ids["through-one"] == ids["spur"] {
		t.Fatalf("cross-way spur merged into the through path: %v", ids)
	}

	for index := range edges {
		edges[index].sourceWay = edges[index].segmentID
	}
	ids = referenceAttributionPathIDs(edges)
	if ids["through-one"] == ids["through-two"] || ids["through-one"] == ids["spur"] || ids["through-two"] == ids["spur"] {
		t.Fatalf("three cross-way branches were merged: %v", ids)
	}
}

func TestConnectedAttributionIdentityRules(t *testing.T) {
	name := "stevens creek trail"
	tests := []struct {
		name  string
		edges []attributionEdge
		merge bool
	}{
		{
			name: "unnamed source lineage",
			edges: []attributionEdge{
				{segmentID: "E5A7797B8C532C72E23085275E2EBA82", logicalID: "10", localityID: "sunnyvale", regionID: "norcal", broadClass: "cycleway", startNode: "a", endNode: "touch"},
				{segmentID: "CAEF72F19BB476189D79274DC9B267EE", logicalID: "20", localityID: "sunnyvale", regionID: "norcal", broadClass: "footway", startNode: "touch", endNode: "b"},
			}, merge: true,
		},
		{
			name: "named path family transition",
			edges: []attributionEdge{
				{segmentID: "cycleway", logicalID: "10", localityID: "sunnyvale", regionID: "norcal", normalizedName: name, broadClass: "cycleway", startNode: "a", endNode: "touch"},
				{segmentID: "footway", logicalID: "20", localityID: "sunnyvale", regionID: "norcal", normalizedName: name, broadClass: "footway", startNode: "touch", endNode: "b"},
			}, merge: true,
		},
		{
			name: "named road path continuation",
			edges: []attributionEdge{
				{segmentID: "road", logicalID: "10", localityID: "sunnyvale", regionID: "norcal", normalizedName: name, broadClass: "road", startNode: "a", endNode: "touch"},
				{segmentID: "footway", logicalID: "20", localityID: "sunnyvale", regionID: "norcal", normalizedName: name, broadClass: "footway", startNode: "touch", endNode: "b"},
			}, merge: true,
		},
		{
			name: "municipality boundary",
			edges: []attributionEdge{
				{segmentID: "sunnyvale", logicalID: "10", localityID: "sunnyvale", regionID: "norcal", normalizedName: name, startNode: "a", endNode: "touch"},
				{segmentID: "cupertino", logicalID: "20", localityID: "cupertino", regionID: "norcal", normalizedName: name, startNode: "touch", endNode: "b"},
			},
		},
		{
			name: "park containment boundary",
			edges: []attributionEdge{
				{segmentID: "park", logicalID: "shared-legacy-id", parkID: "sleeper-park", localityID: "sunnyvale", regionID: "norcal", normalizedName: name, startNode: "a", endNode: "touch"},
				{segmentID: "outside", logicalID: "shared-legacy-id", localityID: "sunnyvale", regionID: "norcal", normalizedName: name, startNode: "touch", endNode: "b"},
			},
		},
		{
			name: "different names",
			edges: []attributionEdge{
				{segmentID: "stevens", logicalID: "10", localityID: "sunnyvale", regionID: "norcal", normalizedName: name, startNode: "a", endNode: "touch"},
				{segmentID: "sleeper", logicalID: "20", localityID: "sunnyvale", regionID: "norcal", normalizedName: "sleeper park path", startNode: "touch", endNode: "b"},
			},
		},
		{
			name: "disconnected logical IDs",
			edges: []attributionEdge{
				{segmentID: "one", logicalID: "10", localityID: "sunnyvale", regionID: "norcal", normalizedName: name, startNode: "a", endNode: "b"},
				{segmentID: "two", logicalID: "20", localityID: "sunnyvale", regionID: "norcal", normalizedName: name, startNode: "c", endNode: "d"},
			},
		},
		{
			name: "named national park across municipality and class",
			edges: []attributionEdge{
				{segmentID: "yosemite-road", logicalID: "10", parkID: "yosemite", localityID: "one", regionID: "norcal", normalizedName: "yosemite lodge drive", broadClass: "road", startNode: "a", endNode: "touch"},
				{segmentID: "yosemite-footway", logicalID: "20", parkID: "yosemite", localityID: "two", regionID: "norcal", normalizedName: "yosemite lodge drive", broadClass: "footway", startNode: "touch", endNode: "b"},
			}, merge: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ids := referenceAttributionPathIDs(test.edges)
			merged := ids[test.edges[0].segmentID] == ids[test.edges[1].segmentID]
			if merged != test.merge {
				t.Fatalf("merged=%t, want %t: %v", merged, test.merge, ids)
			}
		})
	}
}

func TestAttributionIdentityIsInputOrderStable(t *testing.T) {
	edges := []attributionEdge{
		{segmentID: "one", logicalID: "30", localityID: "city", regionID: "norcal", startNode: "a", endNode: "b"},
		{segmentID: "two", logicalID: "10", localityID: "city", regionID: "norcal", startNode: "b", endNode: "c"},
		{segmentID: "three", logicalID: "20", localityID: "city", regionID: "norcal", startNode: "c", endNode: "d"},
	}
	want := referenceAttributionPathIDs(edges)
	sort.Slice(edges, func(i, j int) bool { return edges[i].segmentID > edges[j].segmentID })
	got := referenceAttributionPathIDs(edges)
	for segmentID, wantID := range want {
		if got[segmentID] != wantID {
			t.Fatalf("segment %s ID=%q after reorder, want %q", segmentID, got[segmentID], wantID)
		}
	}
}

func TestDisconnectedMembersOfOneLegacyIDAreSplit(t *testing.T) {
	edges := []attributionEdge{
		{segmentID: "one", logicalID: "legacy", localityID: "city", regionID: "norcal", normalizedName: "old path", startNode: "a", endNode: "b"},
		{segmentID: "two", logicalID: "legacy", localityID: "city", regionID: "norcal", normalizedName: "old path", startNode: "c", endNode: "d"},
	}
	ids := referenceAttributionPathIDs(edges)
	if ids["one"] == ids["two"] || ids["one"] == "legacy" || ids["two"] == "legacy" {
		t.Fatalf("legacy disconnected identity was not split and rewritten: %v", ids)
	}
}
