package helm_test

import (
	"io/fs"
	"path/filepath"
	"testing"
)

const (
	repoDir  = "../.."
	chartDir = "../../helm"
)

func repoPath(path string) string {
	return filepath.Join(repoDir, path)
}

func chartPath(path string) string {
	return filepath.Join(chartDir, path)
}

func TestChartContainsNoGoFiles(t *testing.T) {
	err := filepath.WalkDir(chartDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Ext(path) == ".go" {
			t.Errorf("Go code belongs outside the packaged chart: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
