package coverage

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"
)

func assertParkingVarintKey(t *testing.T, key string, cursor int, portions []TraversedPortion, states *parkingRepairStates) {
	t.Helper()
	remaining := []byte(key)
	read := func(want uint64) {
		t.Helper()
		value, n := binary.Uvarint(remaining)
		if n <= 0 || value != want {
			t.Fatalf("invalid varint: value=%d bytes=%d want=%d", value, n, want)
		}
		if !bytes.Equal(remaining[:n], binary.AppendUvarint(nil, value)) {
			t.Fatal("noncanonical varint")
		}
		remaining = remaining[n:]
	}
	read(uint64(cursor))
	read(uint64(len(portions)))
	count, charged := len(states.portions), states.internBytes
	for _, portion := range portions {
		id, err := states.intern(compiledGraph{}, portion, 0)
		if err != nil {
			t.Fatal(err)
		}
		read(uint64(id))
	}
	if len(remaining) != 0 || len(states.portions) != count || states.internBytes != charged {
		t.Fatal("key has trailing data or does not reference the existing exact dictionary")
	}
}

func TestParkingVarintCursorAndCountBoundaries(t *testing.T) {
	for _, cursor := range []int{-128, -1, 0, 127, 128, 16383, 16384} {
		for _, count := range []int{0, 1, 127, 128, 16383, 16384} {
			t.Run(fmt.Sprintf("cursor=%d/count=%d", cursor, count), func(t *testing.T) {
				states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
				portions := make([]TraversedPortion, count)
				key, err := states.key(compiledGraph{}, portions, cursor)
				if err != nil {
					t.Fatal(err)
				}
				assertParkingVarintKey(t, key, cursor, portions, &states)
				wantBytes := len(binary.AppendUvarint(nil, uint64(cursor))) + len(binary.AppendUvarint(nil, uint64(count))) + count
				if len(key) != wantBytes {
					t.Fatalf("key bytes=%d want=%d", len(key), wantBytes)
				}
			})
		}
	}
}

func TestParkingVarintMaxInternIDs(t *testing.T) {
	states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
	if states.maxInternCount != 16384 {
		t.Fatal("production dictionary bound changed")
	}
	portions := make([]TraversedPortion, states.maxInternCount)
	for i := range portions {
		portions[i].SourceWayID = int64(i + 1)
	}
	key, err := states.key(compiledGraph{}, portions, 0)
	if err != nil {
		t.Fatal(err)
	}
	assertParkingVarintKey(t, key, 0, portions, &states)
	if len(states.portions) != 16384 {
		t.Fatal("test did not populate the actual dictionary to its default bound")
	}
	for _, id := range []int{127, 128, 16383, 16384} {
		got, err := states.key(compiledGraph{}, portions[id-1:id], 0)
		if err != nil {
			t.Fatal(err)
		}
		want := binary.AppendUvarint([]byte{0, 1}, uint64(id))
		if got != string(want) {
			t.Fatalf("ID %d has wrong encoding: %x", id, got)
		}
	}
	charged := states.internBytes
	states.beginCursor(128)
	again, err := states.key(compiledGraph{}, portions, 128)
	if err != nil {
		t.Fatal(err)
	}
	assertParkingVarintKey(t, again, 128, portions, &states)
	if len(states.portions) != 16384 || states.internBytes != charged || again[2:] != key[1:] {
		t.Fatal("cursor reset changed the full dictionary or its stable IDs")
	}
}

func TestParkingVarintOrderedIDsAndFraming(t *testing.T) {
	a, b, c := TraversedPortion{SegmentID: "a"}, TraversedPortion{SegmentID: "b"}, TraversedPortion{SegmentID: "c"}
	states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
	seen := make(map[string]bool)
	for _, test := range []struct {
		cursor   int
		portions []TraversedPortion
	}{
		{0, nil}, {0, []TraversedPortion{a}}, {0, []TraversedPortion{a, b}},
		{0, []TraversedPortion{b, a}}, {0, []TraversedPortion{a, a}},
		{0, []TraversedPortion{a, b, a}}, {0, []TraversedPortion{a, a, b}},
		{1, []TraversedPortion{b, c}}, {2, []TraversedPortion{c}},
	} {
		key, err := states.key(compiledGraph{}, test.portions, test.cursor)
		if err != nil {
			t.Fatal(err)
		}
		assertParkingVarintKey(t, key, test.cursor, test.portions, &states)
		if seen[key] {
			t.Fatal("cursor, count, order, or repeated IDs collided")
		}
		seen[key] = true
		again, err := states.key(compiledGraph{}, test.portions, test.cursor)
		if err != nil || again != key {
			t.Fatalf("same exact state encoded differently: %v", err)
		}
	}
}

func TestParkingVarintActualLengthLimit(t *testing.T) {
	// The old fixed-width size exceeds 512 KiB, but the actual repeated-ID key fits.
	portions := make([]TraversedPortion, (maxParkingRepairStateBytes-16)/4+1)
	states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
	key, err := states.key(compiledGraph{}, portions, 0)
	if err != nil {
		t.Fatal(err)
	}
	assertParkingVarintKey(t, key, 0, portions, &states)
	if 16+4*len(portions) <= states.maxBytes || len(key) != 1+3+len(portions) {
		t.Fatal("fixture does not distinguish actual encoded length from the old guard")
	}
	states.maxBytes = len(key)
	exact, err := states.key(compiledGraph{}, portions, 0)
	if err != nil || exact != key {
		t.Fatalf("exact actual byte bound rejected a fitting key: %v", err)
	}
	if repeated, err := states.remember(exact); repeated || err != nil {
		t.Fatalf("fitting key was not remembered: %v", err)
	}
	if repeated, err := states.remember(exact); !repeated || err != nil {
		t.Fatalf("cycle closer was not recognized at the exact byte bound: %v", err)
	}
	states.beginCursor(0)
	states.maxBytes = len(key) - 1
	_, err = states.key(compiledGraph{}, portions, 0)
	var limit *ParkingRepairLimitError
	if !errors.As(err, &limit) || limit.Field != "parking repair state bytes" ||
		limit.AttemptedKeyBytes != len(key) || limit.ProjectedStateBytes != len(key) || states.count != 1 || states.bytes != 0 {
		t.Fatalf("one-byte-short bound has wrong actual projection or retained a key: %v", err)
	}
}

func TestParkingVarintHeaderAppendLimits(t *testing.T) {
	for _, test := range []struct {
		cursor, count, limit, projected int
	}{
		{128, 0, 1, 2}, {-1, 0, 9, 10}, {0, 128, 2, 3}, {0, 0, 0, 1},
	} {
		states := newParkingRepairStates(maxParkingRepairStates, test.limit)
		_, err := states.key(compiledGraph{}, make([]TraversedPortion, test.count), test.cursor)
		var limit *ParkingRepairLimitError
		if !errors.As(err, &limit) || limit.AttemptedKeyBytes != test.projected || len(states.portions) != 0 {
			t.Fatalf("header append exceeded its byte bound or wrong projection: %v", err)
		}
	}
}

func TestParkingVarint256StatesAtProductionByteLimit(t *testing.T) {
	const portionCount, stateCount, keyBytes = 1086, 256, 2048
	portions := make([]TraversedPortion, portionCount)
	for i := range portions {
		portions[i].SourceWayID = int64(i + 1)
	}
	states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
	var first string
	for i := range stateCount + 1 {
		rotated := append(append([]TraversedPortion(nil), portions[i:]...), portions[:i]...)
		key, err := states.key(compiledGraph{}, rotated, 0)
		if err != nil || len(key) != keyBytes {
			t.Fatalf("production-bound key has wrong size: bytes=%d err=%v", len(key), err)
		}
		if i == 0 {
			first = key
		}
		repeated, err := states.remember(key)
		if i < stateCount {
			if repeated || err != nil {
				t.Fatalf("fitting state %d failed: %v", i, err)
			}
			continue
		}
		var limit *ParkingRepairLimitError
		if repeated || !errors.As(err, &limit) || limit.Field != "parking repair state bytes" ||
			limit.AttemptedKeyBytes != keyBytes || limit.ProjectedStateBytes != maxParkingRepairStateBytes+keyBytes {
			t.Fatalf("production byte limit has wrong projection: %v", err)
		}
	}
	if states.count != stateCount || states.bytes != maxParkingRepairStateBytes || states.maxCount != 4096 {
		t.Fatal("exact byte bound changed the production state count bound or retained a rejected key")
	}
	if repeated, err := states.remember(first); !repeated || err != nil {
		t.Fatalf("duplicate check did not precede the full production byte budget: %v", err)
	}
}

func TestParkingVarintSynthetic508Portions500States(t *testing.T) {
	const portionCount, stateCount = 508, 500
	portions := make([]TraversedPortion, portionCount)
	for i := range portions {
		portions[i].SourceWayID = int64(i + 1)
	}
	states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
	var first string
	for i := range stateCount {
		rotated := append(append([]TraversedPortion(nil), portions[i:]...), portions[:i]...)
		key, err := states.key(compiledGraph{}, rotated, 0)
		if err != nil {
			t.Fatal(err)
		}
		assertParkingVarintKey(t, key, 0, rotated, &states)
		if repeated, err := states.remember(key); repeated || err != nil {
			t.Fatalf("distinct synthetic state %d did not fit: %v", i, err)
		}
		if i == 0 {
			first = key
		}
	}
	const oldBytes = (16 + 4*portionCount) * stateCount
	const actualBytes = (1 + 2 + 127 + 2*(portionCount-127)) * stateCount
	if states.count != stateCount || len(states.portions) != portionCount || states.bytes != actualBytes ||
		oldBytes != 1024000 || states.maxBytes != 512*1024 || states.maxCount != 4096 || states.maxSuffixNodes != 16384 {
		t.Fatal("synthetic payload accounting or production bounds changed")
	}
	states.maxBytes = states.bytes
	if repeated, err := states.remember(first); !repeated || err != nil {
		t.Fatalf("duplicate was rejected with a full retained-byte budget: %v", err)
	}
	t.Logf("SYNTH: portions=%d distinct states=%d retained key bytes=%d old fixed-width reference bytes=%d limit=%d interned portions=%d charged intern bytes=%d", portionCount, stateCount, states.bytes, oldBytes, maxParkingRepairStateBytes, len(states.portions), states.internBytes)
}
