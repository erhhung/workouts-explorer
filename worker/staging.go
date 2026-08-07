package worker

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sys/unix"
)

var stagingChildPattern = regexp.MustCompile(`^ingest-[0-9a-f]{32}$`)

// ScavengeStaging removes app-owned children through a no-follow root descriptor.
func ScavengeStaging(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) {
		return errors.New("unsafe staging root")
	}
	rootFD, err := openStagingRoot(root)
	if err != nil {
		return errors.New("unsafe staging root")
	}
	defer unix.Close(rootFD)
	entries, err := readDirectoryEntries(rootFD)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !stagingChildPattern.MatchString(entry.Name()) {
			continue
		}
		var stat unix.Stat_t
		if err := unix.Fstatat(rootFD, entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return err
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
			continue
		}
		childFD, err := openChildDirectory(rootFD, entry.Name())
		if err != nil {
			if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EXDEV) {
				continue
			}
			return err
		}
		var opened unix.Stat_t
		if err := unix.Fstat(childFD, &opened); err != nil {
			_ = unix.Close(childFD)
			return err
		}
		removeErr := removeDirectoryContents(childFD)
		_ = unix.Close(childFD)
		if removeErr != nil {
			return removeErr
		}
		if err := unlinkOpenedDirectory(rootFD, entry.Name(), opened); err != nil {
			return err
		}
	}
	return nil
}

func openStagingRoot(root string) (int, error) {
	current, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(root, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			_ = unix.Close(current)
			return -1, errors.New("unsafe staging root component")
		}
		next, openErr := openChildDirectory(current, component)
		if errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(current, component, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = unix.Close(current)
				return -1, mkdirErr
			}
			next, openErr = openChildDirectory(current, component)
		}
		_ = unix.Close(current)
		if openErr != nil {
			return -1, openErr
		}
		current = next
	}
	return current, nil
}

func openChildDirectory(parentFD int, name string) (int, error) {
	return unix.Openat2(parentFD, name, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS,
	})
}

func readDirectoryEntries(fd int) ([]os.DirEntry, error) {
	duplicate, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(duplicate), "staging-directory")
	defer directory.Close()
	return directory.ReadDir(-1)
}

func removeDirectoryContents(directoryFD int) error {
	entries, err := readDirectoryEntries(directoryFD)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		var stat unix.Stat_t
		if err := unix.Fstatat(directoryFD, entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return err
		}
		if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
			childFD, err := openChildDirectory(directoryFD, entry.Name())
			if err != nil {
				if errors.Is(err, unix.ENOENT) {
					continue
				}
				return err
			}
			var opened unix.Stat_t
			if err := unix.Fstat(childFD, &opened); err != nil {
				_ = unix.Close(childFD)
				return err
			}
			err = removeDirectoryContents(childFD)
			_ = unix.Close(childFD)
			if err != nil {
				return err
			}
			if err := unlinkOpenedDirectory(directoryFD, entry.Name(), opened); err != nil {
				return err
			}
			continue
		}
		if err := unix.Unlinkat(directoryFD, entry.Name(), 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return err
		}
	}
	return nil
}

func unlinkOpenedDirectory(parentFD int, name string, opened unix.Stat_t) error {
	var current unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	if current.Mode&unix.S_IFMT != unix.S_IFDIR || current.Dev != opened.Dev || current.Ino != opened.Ino {
		return nil
	}
	if err := unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return nil
}
