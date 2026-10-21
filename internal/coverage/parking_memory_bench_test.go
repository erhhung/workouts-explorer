package coverage

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

var parkingMemoryBenchSink string
var parkingMemoryRetainedSink map[string]struct{}

// TEST ONLY: the fixed32 key and intern bodies are copied from the pre-varint
// matcher. Keep their guards, cancellation checks, map lookups and charges intact.
// Embedding supplies unchanged limit diagnostics and remember/beginCursor methods;
// the reference deliberately owns its old intern implementation as well as key.
type parkingMemoryFixed32 struct{ parkingRepairStates }

func (states *parkingMemoryFixed32) intern(graph compiledGraph, portion TraversedPortion, keyBytes int) (uint32, error) {
	if err := graph.err(); err != nil {
		return 0, err
	}
	key := parkingRepairPortionKey{portion: portion, bits: [4]uint64{
		math.Float64bits(portion.FromMeter), math.Float64bits(portion.ToMeter),
		math.Float64bits(portion.SourceFromFraction), math.Float64bits(portion.SourceToFraction),
	}}
	key.portion.FromMeter, key.portion.ToMeter, key.portion.SourceFromFraction, key.portion.SourceToFraction = 0, 0, 0, 0
	if id, exists := states.portions[key]; exists {
		return id, nil
	}
	charge := parkingRepairInternEntryBytes
	for _, value := range [...]string{portion.SegmentID, portion.RegionID, portion.LogicalPathID, portion.LocalityID, string(portion.Direction), portion.ContinuityClass} {
		charge += len(value)
	}
	if len(states.portions) >= states.maxInternCount {
		return 0, states.limitError("parking repair interned portions", keyBytes, charge)
	}
	if charge > states.maxInternBytes-states.internBytes {
		return 0, states.limitError("parking repair intern bytes", keyBytes, charge)
	}
	if states.portions == nil {
		states.portions = make(map[parkingRepairPortionKey]uint32)
	}
	id := uint32(len(states.portions) + 1)
	states.portions[key] = id
	states.internBytes += charge
	return id, nil
}

func (states *parkingMemoryFixed32) key(graph compiledGraph, portions []TraversedPortion, start int) (string, error) {
	if err := graph.err(); err != nil {
		return "", err
	}
	const headerBytes, portionIDBytes = 16, 4
	keyBytes := headerBytes + portionIDBytes*len(portions)
	if keyBytes > states.maxBytes {
		return "", states.limitError("parking repair state bytes", keyBytes, 0)
	}
	key := make([]byte, 0, keyBytes)
	key = binary.LittleEndian.AppendUint64(key, uint64(start))
	key = binary.LittleEndian.AppendUint64(key, uint64(len(portions)))
	var checks uint
	for _, portion := range portions {
		if graph.canceled(&checks) {
			return "", graph.err()
		}
		id, err := states.intern(graph, portion, keyBytes)
		if err != nil {
			return "", err
		}
		key = binary.LittleEndian.AppendUint32(key, id)
	}
	if err := graph.err(); err != nil {
		return "", err
	}
	return string(key), nil
}

const parkingMemoryVariants = 500

func parkingMemoryFixture(size int) [][]TraversedPortion {
	base := make([]TraversedPortion, size)
	for i := range base {
		id := i % min(size, 500)
		base[i] = TraversedPortion{
			SegmentID:     fmt.Sprintf("synthetic-segment-%04d", id),
			RegionID:      "synthetic-region-" + strings.Repeat("r", 64),
			LogicalPathID: "synthetic-path-" + strings.Repeat("p", 64), LocalityID: "synthetic-locality",
			GenerationID: 27, DerivationVersion: 24, SourceWayID: int64(id + 1), SourceWayVersion: 2,
			Direction: SegmentForward, ContinuityClass: "path", FromMeter: 0, ToMeter: 10,
			SourceFromFraction: 0.125, SourceToFraction: 0.875,
		}
		// The large fixture also exercises IDs above 1000, still under the 16k cap.
		if size > 508 {
			base[i].SourceWayID = int64(i + 1)
		}
	}
	variants := make([][]TraversedPortion, parkingMemoryVariants)
	for i := range variants {
		variants[i] = append([]TraversedPortion(nil), base...)
		variants[i][len(base)-1].SourceWayVersion = 1000 + i
	}
	return variants
}

type parkingMemoryEncoder struct {
	state *parkingRepairStates
	key   func(compiledGraph, []TraversedPortion, int) (string, error)
}

func newParkingMemoryEncoder(tb testing.TB, fixed bool, maxBytes int, fixtures [][]TraversedPortion) parkingMemoryEncoder {
	tb.Helper()
	state := newParkingRepairStates(maxParkingRepairStates, maxBytes)
	encoder := parkingMemoryEncoder{}
	if fixed {
		reference := &parkingMemoryFixed32{state}
		encoder.state, encoder.key = &reference.parkingRepairStates, reference.key
	} else {
		encoder.state, encoder.key = &state, state.key
	}
	graph := compiledGraph{ctx: context.Background()}
	for _, portions := range fixtures {
		if _, err := encoder.key(graph, portions, 0); err != nil {
			tb.Fatal(err)
		}
	}
	return encoder
}

// Each operation encodes exactly 500 finite variants using already-populated
// dictionaries. EncodeOnly never remembers keys: it measures encoding, not a
// cap-triggered early exit, fresh map construction or continuation-cache growth.
func BenchmarkParkingStateKeyMemory(b *testing.B) {
	for _, size := range []int{96, 508, 2003} {
		fixtures := parkingMemoryFixture(size)
		b.Run(fmt.Sprintf("Portions%d", size), func(b *testing.B) {
			for _, retained := range []bool{false, true} {
				if retained && size != 508 {
					continue
				}
				class := "EncodeOnly"
				if retained {
					class = "RetainedKeys"
				}
				b.Run(class, func(b *testing.B) {
					for _, fixed := range []bool{true, false} {
						name := "Compact_varint"
						if fixed {
							name = "Fixed32"
						}
						b.Run(name, func(b *testing.B) {
							maxBytes := maxParkingRepairStateBytes
							if retained && fixed {
								// Diagnostic reference capacity only, NOT a production limit change.
								maxBytes = 2 * 1024 * 1024
							}
							encoder := newParkingMemoryEncoder(b, fixed, maxBytes, fixtures)
							graph := compiledGraph{ctx: context.Background()}
							interned, internBytes := len(encoder.state.portions), encoder.state.internBytes
							var totalBytes int64
							b.ReportAllocs()
							b.ResetTimer()
							for b.Loop() {
								var keys map[string]struct{}
								if retained {
									keys = make(map[string]struct{}, len(fixtures))
								}
								for _, portions := range fixtures {
									key, err := encoder.key(graph, portions, 0)
									if err != nil {
										b.Fatal(err)
									}
									parkingMemoryBenchSink = key
									totalBytes += int64(len(key))
									if retained {
										keys[key] = struct{}{}
									}
								}
								if retained {
									parkingMemoryRetainedSink = keys
									runtime.KeepAlive(keys)
								}
							}
							b.StopTimer()
							b.ReportMetric(float64(totalBytes)/float64(b.N), "key-bytes/op")
							b.ReportMetric(float64(len(fixtures)), "keys/op")
							if retained && len(parkingMemoryRetainedSink) != len(fixtures) {
								b.Fatal("retained keys are not distinct")
							}
							if encoder.state.count != 0 || encoder.state.bytes != 0 || len(encoder.state.portions) != interned || encoder.state.internBytes != internBytes || len(encoder.state.suffixes) != 0 || len(encoder.state.continuations) != 0 {
								b.Fatal("encoding grew tracking state after prewarming")
							}
						})
					}
				})
			}
		})
	}
}

// Live heap is sampled separately from benchmark timing/allocation counters.
// The private runner repeats this test in five fresh processes. Only synthetic
// numeric summaries are logged; fixture metadata and keys are never printed.
func TestParkingStateKeyMemoryHeap(t *testing.T) {
	fixtures := parkingMemoryFixture(508)
	for _, fixed := range []bool{true, false} {
		name, maxBytes := "Compact_varint", maxParkingRepairStateBytes
		if fixed {
			name, maxBytes = "Fixed32", 2*1024*1024
		}
		t.Run(name, func(t *testing.T) {
			encoder := newParkingMemoryEncoder(t, fixed, maxBytes, fixtures)
			graph := compiledGraph{ctx: context.Background()}
			parkingMemoryBenchSink, parkingMemoryRetainedSink = "", nil
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			keys := make(map[string]struct{}, len(fixtures))
			total := 0
			for _, portions := range fixtures {
				key, err := encoder.key(graph, portions, 0)
				if err != nil {
					t.Fatal(err)
				}
				total += len(key)
				keys[key] = struct{}{}
			}
			runtime.GC()
			runtime.ReadMemStats(&after)
			runtime.KeepAlive(keys)
			runtime.KeepAlive(encoder)
			runtime.KeepAlive(fixtures)
			if len(keys) != parkingMemoryVariants || len(encoder.state.portions) != 1000 || len(encoder.state.suffixes) != 0 || len(encoder.state.continuations) != 0 {
				t.Fatal("retained fixture cardinality or cache isolation changed")
			}
			record, err := json.Marshal(struct {
				Keys          int   `json:"keys/op"`
				Portions      int   `json:"portions"`
				Interned      int   `json:"interned"`
				KeyBytes      int   `json:"key-bytes/op"`
				MaxStateBytes int   `json:"max-state-bytes"`
				HeapBytes     int64 `json:"heap-bytes/op"`
			}{len(keys), 508, len(encoder.state.portions), total, maxBytes, int64(after.HeapAlloc) - int64(before.HeapAlloc)})
			if err != nil {
				t.Fatal(err)
			}
			t.Log(string(record))
		})
	}
}

// Decode to the same semantic vector; byte strings from different formats must
// never be compared directly. Both formats describe cursor, count and exact IDs.
func parkingMemoryDecode(t *testing.T, key string, fixed bool) []uint64 {
	t.Helper()
	data := []byte(key)
	if fixed {
		if len(data) < 16 || (len(data)-16)%4 != 0 {
			t.Fatal("invalid fixed32 key")
		}
		out := []uint64{binary.LittleEndian.Uint64(data), binary.LittleEndian.Uint64(data[8:])}
		for offset := 16; offset < len(data); offset += 4 {
			out = append(out, uint64(binary.LittleEndian.Uint32(data[offset:])))
		}
		if out[1] != uint64(len(out)-2) {
			t.Fatal("fixed32 count mismatch")
		}
		return out
	}
	var out []uint64
	for len(data) > 0 {
		value, n := binary.Uvarint(data)
		if n <= 0 || n != len(binary.AppendUvarint(nil, value)) {
			t.Fatal("invalid unsigned varint key")
		}
		out = append(out, value)
		data = data[n:]
	}
	// V1 encodes cursor, count, IDs, without an additional format marker.
	if len(out) < 2 || out[1] != uint64(len(out)-2) {
		t.Fatal("varint count mismatch")
	}
	return out
}

func TestParkingStateKeyMemoryEquivalence(t *testing.T) {
	fixtures := parkingMemoryFixture(508)
	fixed := newParkingMemoryEncoder(t, true, maxParkingRepairStateBytes, fixtures)
	compact := newParkingMemoryEncoder(t, false, maxParkingRepairStateBytes, fixtures)
	graph := compiledGraph{ctx: context.Background()}
	if len(fixed.state.portions) != 1000 || len(compact.state.portions) != 1000 {
		t.Fatal("508-portion fixture must intern exactly 1000 values")
	}
	rng := rand.New(rand.NewPCG(71, 93))
	oldSeen, newSeen, truthSeen := map[string]bool{}, map[string]bool{}, map[string]bool{}
	var savedPortions []TraversedPortion
	var savedStart int
	for i := range 2000 {
		portions := fixtures[rng.IntN(len(fixtures))]
		if i%20 != 0 {
			// Mix lengths/order without retaining gigabytes of full-field truth.
			short := append([]TraversedPortion(nil), portions[:rng.IntN(16)+1]...)
			portions = append(short, portions[len(portions)-1])
		}
		start := []int{0, 1, 127, 128, 16383, 16384, -1}[rng.IntN(7)]
		if i%3 != 0 {
			portions = append([]TraversedPortion(nil), portions...)
			p := &portions[rng.IntN(len(portions))]
			switch i % 10 {
			case 0:
				p.FromMeter = math.Copysign(0, -1)
			case 1:
				p.ToMeter = math.Float64frombits(0x7ff8000000000000 + uint64(i%7))
			case 2:
				p.SourceFromFraction = math.Copysign(0, -1)
			case 3:
				p.SourceToFraction = math.Float64frombits(0x7ff8000000000001 + uint64(i%11))
			case 4:
				p.SourceWayID++
			case 5:
				p.SourceWayVersion++
			case 6:
				p.RegionID += "-different"
			case 7:
				p.PhysicalSegmentID[0]++
			case 8:
				p.Direction = SegmentReverse
			case 9:
				p.GenerationID++
			}
			rng.Shuffle(len(portions), func(i, j int) { portions[i], portions[j] = portions[j], portions[i] })
		}
		if i%4 == 0 && i > 0 {
			// Re-encode actual duplicate inputs, not just previously saved keys.
			portions, start = savedPortions, savedStart
		}
		oldKey, err := fixed.key(graph, portions, start)
		if err != nil {
			t.Fatal(err)
		}
		newKey, err := compact.key(graph, portions, start)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(parkingMemoryDecode(t, oldKey, true), parkingMemoryDecode(t, newKey, false)) {
			t.Fatal("decoded semantic vectors differ")
		}
		// Independent truth uses full fields and raw float bits, not intern IDs.
		truth := fmt.Sprintf("%d/%d/", start, len(portions))
		var builder strings.Builder
		builder.WriteString(truth)
		for _, p := range portions {
			bits := [4]uint64{math.Float64bits(p.FromMeter), math.Float64bits(p.ToMeter), math.Float64bits(p.SourceFromFraction), math.Float64bits(p.SourceToFraction)}
			p.FromMeter, p.ToMeter, p.SourceFromFraction, p.SourceToFraction = 0, 0, 0, 0
			fmt.Fprintf(&builder, "%#v/%x;", p, bits)
		}
		truth = builder.String()
		if oldSeen[oldKey] != truthSeen[truth] || newSeen[newKey] != truthSeen[truth] {
			t.Fatal("exact state membership differs from full-field truth")
		}
		oldSeen[oldKey], newSeen[newKey], truthSeen[truth] = true, true, true
		savedPortions, savedStart = portions, start
	}
	if len(fixed.state.portions) != len(compact.state.portions) || fixed.state.internBytes != compact.state.internBytes {
		t.Fatal("dictionary membership/accounting differs")
	}
}

func TestParkingStateKeyMemoryRetentionLimit(t *testing.T) {
	fixtures := parkingMemoryFixture(508)
	graph := compiledGraph{ctx: context.Background()}
	for _, fixed := range []bool{true, false} {
		encoder := newParkingMemoryEncoder(t, fixed, maxParkingRepairStateBytes, fixtures)
		total, accepted, failures := 0, 0, 0
		for _, portions := range fixtures {
			key, err := encoder.key(graph, portions, 0)
			if err != nil {
				t.Fatal(err)
			}
			total += len(key)
			repeated, err := encoder.state.remember(key)
			if err != nil {
				var limit *ParkingRepairLimitError
				if !errors.As(err, &limit) || limit.Field != "parking repair state bytes" {
					t.Fatal("unexpected retention error")
				}
				failures++
			} else if repeated {
				t.Fatal("synthetic variants are not unique")
			} else {
				accepted++
			}
		}
		if fixed && (total != 1024000 || accepted != 256 || failures != 244) {
			t.Fatal("fixed32 stress no longer crosses the 512 KiB limit")
		}
		if !fixed && (total > maxParkingRepairStateBytes || accepted != 500 || failures != 0) {
			t.Fatal("compact stress did not fit the unchanged production limit")
		}
		if len(encoder.state.suffixes) != 0 || len(encoder.state.continuations) != 0 {
			t.Fatal("key encoding populated continuation caches")
		}
	}
}
