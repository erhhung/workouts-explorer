package routepipeline

import (
	"bytes"
	"container/list"
	"context"
	"math"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
)

const snapshotCacheBytes = 8 << 20
const snapshotCacheEntryBytes = 256
const snapshotCacheRowBytes = 512

type memoEntry[K comparable, V any] struct {
	key   K
	value V
	bytes int
}

type boundedMemo[K comparable, V any] struct {
	entries map[K]*list.Element
	order   list.List
	bytes   int
	limit   int
	slots   int
}

func (memo *boundedMemo[K, V]) get(key K) (V, bool) {
	if element := memo.entries[key]; element != nil {
		memo.order.MoveToFront(element)
		return element.Value.(memoEntry[K, V]).value, true
	}
	var zero V
	return zero, false
}

func (memo *boundedMemo[K, V]) put(key K, value V, size int) {
	if size > memo.limit || memo.slots < 1 {
		return
	}
	if memo.entries == nil {
		memo.entries = make(map[K]*list.Element)
	}
	remove := func(element *list.Element) {
		entry := element.Value.(memoEntry[K, V])
		memo.bytes -= entry.bytes
		delete(memo.entries, entry.key)
		memo.order.Remove(element)
	}
	if old := memo.entries[key]; old != nil {
		remove(old)
	}
	for memo.bytes+size > memo.limit || len(memo.entries) >= memo.slots {
		remove(memo.order.Back())
	}
	memo.entries[key] = memo.order.PushFront(memoEntry[K, V]{key, value, size})
	memo.bytes += size
}

type observationKey struct {
	longitude, latitude, radius uint64
	limit                       int
}

func keyForObservation(point osm.MatcherObservation, limit int) observationKey {
	return observationKey{math.Float64bits(point.Longitude), math.Float64bits(point.Latitude), math.Float64bits(point.RadiusMeters), limit}
}

// A cache belongs to one immutable, pinned route snapshot. Only complete successful
// lookups are retained; every partial miss delegates the unchanged original request.
type cachedSnapshot struct {
	Snapshot
	observations boundedMemo[observationKey, []osm.MatcherCandidate]
	nodes        boundedMemo[uuid.UUID, []osm.MatcherIncidentEdge]
}

func newCachedSnapshot(snapshot Snapshot) *cachedSnapshot {
	return &cachedSnapshot{Snapshot: snapshot,
		observations: boundedMemo[observationKey, []osm.MatcherCandidate]{limit: snapshotCacheBytes, slots: 512},
		nodes:        boundedMemo[uuid.UUID, []osm.MatcherIncidentEdge]{limit: snapshotCacheBytes, slots: 4096}}
}

func copiedPointer[T any](pointer *T) *T {
	if pointer == nil {
		return nil
	}
	value := *pointer
	return &value
}

func cloneCandidate(value osm.MatcherCandidate) osm.MatcherCandidate {
	value.Tags = bytes.Clone(value.Tags)
	value.Name, value.NormalizedName = copiedPointer(value.Name), copiedPointer(value.NormalizedName)
	value.LocalityRelationID, value.TangentDegrees = copiedPointer(value.LocalityRelationID), copiedPointer(value.TangentDegrees)
	return value
}

func cloneEdge(value osm.MatcherIncidentEdge) osm.MatcherIncidentEdge {
	value.Tags = bytes.Clone(value.Tags)
	value.Name, value.NormalizedName = copiedPointer(value.Name), copiedPointer(value.NormalizedName)
	value.LocalityRelationID = copiedPointer(value.LocalityRelationID)
	return value
}

func rowCharge(region, highway, class string, tags []byte, name, normalized *string) int {
	charge := snapshotCacheRowBytes + len(region) + len(highway) + len(class) + len(tags)
	for _, value := range []*string{name, normalized} {
		if value != nil {
			charge += len(*value)
		}
	}
	return charge
}

func (snapshot *cachedSnapshot) Candidates(ctx context.Context, points []osm.MatcherObservation, limit int) ([]osm.MatcherCandidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(points) == 0 || len(points) > 256 || limit < 1 || limit > 256 {
		return snapshot.Snapshot.Candidates(ctx, points, limit)
	}
	complete := true
	groups := make([][]osm.MatcherCandidate, len(points))
	for i, point := range points {
		var found bool
		groups[i], found = snapshot.observations.get(keyForObservation(point, limit))
		complete = complete && found
	}
	if complete {
		var result []osm.MatcherCandidate
		for i, group := range groups {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for _, row := range group {
				row = cloneCandidate(row)
				row.ObservationIndex = i
				result = append(result, row)
			}
		}
		return result, nil
	}
	rows, err := snapshot.Snapshot.Candidates(ctx, points, limit)
	if err != nil {
		return nil, err
	}
	groups = make([][]osm.MatcherCandidate, len(points))
	for _, row := range rows {
		if row.ObservationIndex < 0 || row.ObservationIndex >= len(groups) {
			return rows, nil
		}
		groups[row.ObservationIndex] = append(groups[row.ObservationIndex], row)
	}
	for i, group := range groups {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		charge := snapshotCacheEntryBytes
		for _, row := range group {
			charge += rowCharge(row.RegionID, row.Highway, row.BroadClass, row.Tags, row.Name, row.NormalizedName)
		}
		if charge > snapshot.observations.limit {
			continue
		}
		copy := make([]osm.MatcherCandidate, len(group))
		for j, row := range group {
			copy[j] = cloneCandidate(row)
		}
		snapshot.observations.put(keyForObservation(points[i], limit), copy, charge)
	}
	return rows, nil
}

func (snapshot *cachedSnapshot) IncidentEdges(ctx context.Context, nodes []uuid.UUID, limit int) ([]osm.MatcherIncidentEdge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(nodes) == 0 || len(nodes) > 256 || limit < 1 || limit > 4096 {
		return snapshot.Snapshot.IncidentEdges(ctx, nodes, limit)
	}
	requested := make(map[uuid.UUID]int, len(nodes))
	for i, node := range nodes {
		if _, duplicate := requested[node]; duplicate || node == uuid.Nil {
			return snapshot.Snapshot.IncidentEdges(ctx, nodes, limit)
		}
		requested[node] = i
	}
	complete := true
	groups := make([][]osm.MatcherIncidentEdge, len(nodes))
	count := 0
	for i, node := range nodes {
		var found bool
		groups[i], found = snapshot.nodes.get(node)
		complete = complete && found
		count += len(groups[i])
	}
	if complete && count <= limit {
		var result []osm.MatcherIncidentEdge
		for _, group := range groups {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for _, row := range group {
				result = append(result, cloneEdge(row))
			}
		}
		return result, nil
	}
	rows, err := snapshot.Snapshot.IncidentEdges(ctx, nodes, limit)
	if err != nil {
		return nil, err
	}
	groups = make([][]osm.MatcherIncidentEdge, len(nodes))
	for _, row := range rows {
		i, valid := requested[row.RequestedNodeID]
		if !valid {
			return rows, nil
		}
		groups[i] = append(groups[i], row)
	}
	for i, group := range groups {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		charge := snapshotCacheEntryBytes
		for _, row := range group {
			charge += rowCharge(row.RegionID, row.Highway, row.BroadClass, row.Tags, row.Name, row.NormalizedName)
		}
		if charge > snapshot.nodes.limit {
			continue
		}
		copy := make([]osm.MatcherIncidentEdge, len(group))
		for j, row := range group {
			copy[j] = cloneEdge(row)
		}
		snapshot.nodes.put(nodes[i], copy, charge)
	}
	return rows, nil
}
