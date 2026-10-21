package rclonesource

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func encodedList(t *testing.T, items ...any) []byte {
	t.Helper()
	data, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestMetadataValidation(t *testing.T) {
	s := &Source{namespace: "test-namespace"}
	name := "HealthAutoExport-2026-01-01.json"
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"missing-id", func(m map[string]any) { delete(m, "ID") }},
		{"null-id", func(m map[string]any) { m["ID"] = nil }},
		{"empty-id", func(m map[string]any) { m["ID"] = "" }},
		{"control-id", func(m map[string]any) { m["ID"] = "id\x00private" }},
		{"surrogate-id", func(m map[string]any) { m["ID"] = "id\ufffd" }},
		{"large-id", func(m map[string]any) { m["ID"] = strings.Repeat("x", 4097) }},
		{"missing-size", func(m map[string]any) { delete(m, "Size") }},
		{"null-size", func(m map[string]any) { m["Size"] = nil }},
		{"negative-size", func(m map[string]any) { m["Size"] = -1 }},
		{"oversized", func(m map[string]any) { m["Size"] = maxFile + 1 }},
		{"float-size", func(m map[string]any) { m["Size"] = 1.5 }},
		{"string-size", func(m map[string]any) { m["Size"] = "5" }},
		{"missing-mtime", func(m map[string]any) { delete(m, "ModTime") }},
		{"zero-mtime", func(m map[string]any) { m["ModTime"] = "0001-01-01T00:00:00Z" }},
		{"bad-mtime", func(m map[string]any) { m["ModTime"] = "yesterday" }},
		{"missing-isdir", func(m map[string]any) { delete(m, "IsDir") }},
		{"null-isdir", func(m map[string]any) { m["IsDir"] = nil }},
		{"traversal", func(m map[string]any) { m["Path"] = "../" + name }},
		{"nested-path", func(m map[string]any) { m["Path"] = "folder/" + name }},
		{"mismatch", func(m map[string]any) { m["Path"] = "different.json" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := helperItem(name)
			test.change(item)
			_, err := s.parseList(encodedList(t, item))
			assertCode(t, err, "source-file-invalid")
		})
	}
	for _, name := range []string{"../private", "/absolute", "a/b", `a\b`, "a:remote", ".", "..", "private\x00", "private\n", strings.Repeat("x", 256)} {
		item := helperItem(name)
		_, err := s.parseList(encodedList(t, item))
		assertCode(t, err, "source-file-invalid")
	}
	for _, data := range []string{
		`null`, `{}`, `[] {}`, `[null]`, `[`, `[{"Name":"a","Name":"b","Path":"a","IsDir":false}]`,
		`[{"Name":"a","Path":"a","IsDir":false,"Unknown":1,"Unknown":2}]`,
	} {
		_, err := s.parseList([]byte(data))
		assertCode(t, err, "source-file-invalid")
	}
}

func TestListEligibilityAndDuplicateDetection(t *testing.T) {
	s := &Source{namespace: "namespace"}
	var items []any
	for _, name := range []string{"HealthAutoExport-2026-02-29.json", "HealthAutoExport-2026-13-01.json", "HealthAutoExport-2026-01-32.json", "HealthAutoExport-0000-01-01.json", "HealthAutoExport-2026-1-01.json", "notes.json", "HealthAutoExport-2026-01-01.JSON"} {
		items = append(items, helperItem(name))
	}
	directory := helperItem("HealthAutoExport-2026-01-03.json")
	directory["IsDir"] = true
	delete(directory, "ID")
	delete(directory, "Size")
	delete(directory, "ModTime")
	items = append(items, directory, helperItem("HealthAutoExport-2026-01-02.json"), helperItem("HealthAutoExport-2024-02-29.json"))
	objects, err := s.parseList(encodedList(t, items...))
	if err != nil || len(objects) != 2 || objects[0].Name != "HealthAutoExport-2024-02-29.json" {
		t.Fatalf("eligibility: %d, %v", len(objects), err)
	}
	for _, data := range [][]byte{
		encodedList(t, helperItem("notes"), helperItem("notes")),
		encodedList(t, helperItem("HealthAutoExport-2026-01-01.json"), helperItem("HealthAutoExport-2026-01-01.JSON")),
	} {
		_, err := s.parseList(data)
		assertCode(t, err, "source-file-invalid")
	}
	first, second := helperItem("HealthAutoExport-2026-01-01.json"), helperItem("HealthAutoExport-2026-01-02.json")
	second["ID"] = first["ID"]
	_, err = s.parseList(encodedList(t, first, second))
	assertCode(t, err, "source-file-invalid")
}

func TestDirectoryLimits(t *testing.T) {
	s := &Source{namespace: "namespace"}
	for _, eligible := range []bool{false, true} {
		t.Run(fmt.Sprint(eligible), func(t *testing.T) {
			limit := maxEntries
			if eligible {
				limit = maxEligible
			}
			var data strings.Builder
			data.WriteByte('[')
			date := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
			for i := 0; i <= limit; i++ {
				if i > 0 {
					data.WriteByte(',')
				}
				if eligible {
					name := "HealthAutoExport-" + date.AddDate(0, 0, i).Format("2006-01-02") + ".json"
					fmt.Fprintf(&data, `{"Name":%q,"Path":%q,"IsDir":false,"ID":%q,"Size":1,"ModTime":"2026-01-01T00:00:00Z"}`, name, name, fmt.Sprint(i))
				} else {
					fmt.Fprintf(&data, `{"Name":"ignored-%d","Path":"ignored-%d","IsDir":false}`, i, i)
				}
			}
			data.WriteByte(']')
			_, err := s.parseList([]byte(data.String()))
			assertCode(t, err, "source-directory-limit")
		})
	}
	_, err := s.parseList([]byte(strings.Repeat(" ", maxOutput+1)))
	assertCode(t, err, "source-directory-limit")
}

func TestIdentityStabilityAndNamespace(t *testing.T) {
	item := helperItem("HealthAutoExport-2026-01-01.json")
	s := &Source{namespace: "account-a-path-a-client-a"}
	objects, err := s.parseList(encodedList(t, item))
	if err != nil {
		t.Fatal(err)
	}
	identity := objects[0].Identity
	if !strings.HasPrefix(identity, "v2:rclone:") || len(identity) != len("v2:rclone:")+64 {
		t.Fatal("identity format")
	}
	item["ModTime"] = "2026-01-02T19:00:00-05:00"
	again, err := s.parseList(encodedList(t, item))
	if err != nil || again[0].Identity != identity {
		t.Fatal("mtime timezone changed identity")
	}
	for _, alter := range []func(*Object){func(o *Object) { o.ID += "-replacement" }, func(o *Object) { o.Size++ }, func(o *Object) { o.Modified = o.Modified.Add(time.Second) }} {
		object := objects[0]
		alter(&object)
		if s.objectIdentity(object) == identity {
			t.Fatal("metadata change preserved identity")
		}
	}
	s.namespace = "account-a-path-b-client-a"
	if s.objectIdentity(objects[0]) == identity {
		t.Fatal("namespace change preserved identity")
	}
}
