package rclonesource

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type metadata struct {
	name     string
	path     string
	id       string
	size     *int64
	modified string
	isDir    bool
}

// Decode selected fields explicitly so duplicate keys and case-folded JSON
// aliases cannot turn an unsafe path or absent metadata into an accepted file.
func decodeMetadata(raw json.RawMessage) (metadata, error) {
	if !utf8.Valid(raw) {
		return metadata{}, errors.New("metadata")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return metadata{}, errors.New("metadata")
	}
	var item metadata
	seen := make(map[string]bool)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return metadata{}, err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return metadata{}, errors.New("metadata")
		}
		seen[key] = true
		var value json.RawMessage
		if err = decoder.Decode(&value); err != nil {
			return metadata{}, err
		}
		switch key {
		case "Name":
			err = json.Unmarshal(value, &item.name)
		case "Path":
			err = json.Unmarshal(value, &item.path)
		case "ID":
			err = json.Unmarshal(value, &item.id)
		case "Size":
			err = json.Unmarshal(value, &item.size)
		case "ModTime":
			err = json.Unmarshal(value, &item.modified)
		case "IsDir":
			if bytes.Equal(value, []byte("null")) {
				return metadata{}, errors.New("metadata")
			}
			err = json.Unmarshal(value, &item.isDir)
		}
		if err != nil {
			return metadata{}, err
		}
	}
	if _, err = decoder.Token(); err != nil {
		return metadata{}, err
	}
	if !seen["Name"] || !seen["Path"] || !seen["IsDir"] {
		return metadata{}, errors.New("metadata")
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return metadata{}, errors.New("metadata")
	}
	return item, nil
}

func safeBase(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 && utf8.ValidString(name) &&
		!strings.ContainsAny(name, "/\\:") && !strings.ContainsFunc(name, unicode.IsControl)
}

func eligibleName(name string) bool {
	const prefix = "HealthAutoExport-"
	if !safeBase(name) || len(name) != len(prefix)+10+len(".json") || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
		return false
	}
	date := name[len(prefix) : len(prefix)+10]
	_, err := time.Parse("2006-01-02", date)
	return err == nil && !strings.HasPrefix(date, "0000-")
}

func validObject(object Object) bool {
	return object.ID != "" && len(object.ID) <= 4096 && utf8.ValidString(object.ID) &&
		!strings.ContainsFunc(object.ID, unicode.IsControl) && !strings.ContainsRune(object.ID, unicode.ReplacementChar) &&
		object.Size >= 0 && object.Size <= maxFile &&
		!object.Modified.IsZero() && object.Modified.Year() >= 1 && object.Modified.Year() <= 9999
}

func (s *Source) objectIdentity(object Object) string {
	encoded, _ := json.Marshal([]any{s.namespace, object.ID, object.Size, object.Modified.UTC().Format(time.RFC3339Nano)})
	hash := sha256.Sum256(encoded)
	return "v2:rclone:" + hex.EncodeToString(hash[:])
}

func matches(item metadata, object Object) bool {
	modified, err := time.Parse(time.RFC3339Nano, item.modified)
	return err == nil && !item.isDir && item.path == object.Name && item.name == object.Name &&
		item.id == object.ID && item.size != nil && *item.size == object.Size && modified.Equal(object.Modified)
}

func (s *Source) parseList(data []byte) ([]Object, error) {
	if len(data) > maxOutput {
		return nil, failure("source-directory-limit", nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return nil, failure("source-file-invalid", nil)
	}
	objects := make([]Object, 0)
	paths := make(map[string]bool)
	ids := make(map[string]bool)
	for entries := 0; decoder.More(); entries++ {
		if entries >= maxEntries {
			return nil, failure("source-directory-limit", nil)
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, failure("source-file-invalid", nil)
		}
		item, err := decodeMetadata(raw)
		if err != nil || !safeBase(item.name) || item.path != item.name {
			return nil, failure("source-file-invalid", nil)
		}
		// iCloud path lookups are case insensitive. Reject ambiguous aliases even
		// when one entry would otherwise be ignored by the export filter.
		key := strings.ToLower(item.path)
		if paths[key] {
			return nil, failure("source-file-invalid", nil)
		}
		paths[key] = true
		if item.isDir || !eligibleName(item.name) {
			continue
		}
		if len(objects) >= maxEligible {
			return nil, failure("source-directory-limit", nil)
		}
		modified, err := time.Parse(time.RFC3339Nano, item.modified)
		if err != nil || item.size == nil {
			return nil, failure("source-file-invalid", nil)
		}
		object := Object{Name: item.name, ID: item.id, Size: *item.size, Modified: modified.UTC()}
		if !validObject(object) || ids[object.ID] {
			return nil, failure("source-file-invalid", nil)
		}
		ids[object.ID] = true
		object.Identity = s.objectIdentity(object)
		objects = append(objects, object)
	}
	if _, err = decoder.Token(); err != nil {
		return nil, failure("source-file-invalid", nil)
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, failure("source-file-invalid", nil)
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].Name < objects[j].Name })
	return objects, nil
}
