package sourceconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const LocalType = "health-auto-export-local"

type Local struct {
	Version int    `json:"version"`
	Path    string `json:"path"`
}

func CanonicalizeLocal(config Local, approvedRoots []string) (Local, []byte, error) {
	if config.Version != 1 {
		return Local{}, nil, errors.New("local source config version must be 1")
	}
	path, err := ValidatePath(config.Path, approvedRoots)
	if err != nil {
		return Local{}, nil, err
	}
	canonical := Local{Version: 1, Path: path}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return Local{}, nil, errors.New("encode local source configuration")
	}
	return canonical, encoded, nil
}

func DecodeLocal(encoded []byte, approvedRoots []string) (Local, []byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var config Local
	if err := decoder.Decode(&config); err != nil {
		return Local{}, nil, errors.New("invalid local source configuration")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Local{}, nil, errors.New("invalid local source configuration")
	}
	return CanonicalizeLocal(config, approvedRoots)
}

func ValidateRoots(roots []string) ([]string, error) {
	if len(roots) == 0 {
		return nil, errors.New("at least one local source root is required")
	}
	canonical := make([]string, 0, len(roots))
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		if !validPathString(root) || !filepath.IsAbs(root) {
			return nil, errors.New("local source roots must be absolute valid paths")
		}
		clean := filepath.Clean(root)
		if clean == string(filepath.Separator) {
			return nil, errors.New("the filesystem root cannot be an approved source root")
		}
		if _, exists := seen[clean]; exists {
			return nil, errors.New("local source roots must be unique")
		}
		for _, existing := range canonical {
			if containedBy(clean, existing) || containedBy(existing, clean) {
				return nil, errors.New("local source roots must not overlap")
			}
		}
		seen[clean] = struct{}{}
		canonical = append(canonical, clean)
	}
	return canonical, nil
}

func ValidatePath(path string, approvedRoots []string) (string, error) {
	if !validPathString(path) || !filepath.IsAbs(path) {
		return "", errors.New("local source path must be an absolute valid path")
	}
	roots, err := ValidateRoots(approvedRoots)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(path)
	for _, root := range roots {
		if containedBy(clean, root) {
			return clean, nil
		}
	}
	return "", errors.New("local source path must be within an approved root")
}

// OpenDirectory returns the already-validated descriptor the worker must use for discovery.
func OpenDirectory(path string, approvedRoots []string) (*os.File, error) {
	clean, err := ValidatePath(path, approvedRoots)
	if err != nil {
		return nil, err
	}
	for _, root := range approvedRoots {
		relative, err := filepath.Rel(root, clean)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		rootFD, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			continue
		}
		fd, openErr := unix.Openat2(rootFD, relative, &unix.OpenHow{
			Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
			Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS,
		})
		_ = unix.Close(rootFD)
		if openErr == nil {
			return os.NewFile(uintptr(fd), "local-source"), nil
		}
	}
	return nil, errors.New("local source path is not an accessible approved directory")
}

// OpenRegularFile opens one direct child without following links or re-resolving a pathname.
func OpenRegularFile(directory *os.File, name string) (*os.File, os.FileInfo, error) {
	if directory == nil || !validPathString(name) || name == "." || name == ".." || filepath.Base(name) != name {
		return nil, nil, errors.New("source file name is invalid")
	}
	fd, err := unix.Openat2(int(directory.Fd()), name, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return nil, nil, errors.New("source file is unavailable")
	}
	file := os.NewFile(uintptr(fd), "source-file")
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, errors.New("source file is not regular")
	}
	return file, info, nil
}

func ParseRoots(encoded string) ([]string, error) {
	if strings.TrimSpace(encoded) == "" {
		return nil, errors.New("LOCAL_SOURCE_ROOTS is required")
	}
	parts := strings.Split(encoded, ",")
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	roots, err := ValidateRoots(parts)
	if err != nil {
		return nil, fmt.Errorf("LOCAL_SOURCE_ROOTS is invalid: %w", err)
	}
	return roots, nil
}

func containedBy(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func validPathString(path string) bool {
	return path != "" && utf8.ValidString(path) && !strings.ContainsRune(path, 0)
}
