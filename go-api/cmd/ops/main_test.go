package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestPostgresSQLFilesDoNotContainInlineMySQLIndexes(t *testing.T) {
	t.Parallel()

	inlineIndexPattern := regexp.MustCompile(`,\s*(INDEX|KEY)\s+`)
	for _, name := range []string{"install.pgsql.sql", "update.pgsql.sql"} {
		path := filepath.Join(repoRootFromTestFile(t), "database", name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		lines := strings.Split(string(raw), "\n")
		for idx, line := range lines {
			if inlineIndexPattern.MatchString(line) {
				t.Fatalf("%s:%d contains unsupported inline index syntax: %s", path, idx+1, strings.TrimSpace(line))
			}
		}
	}
}

func repoRootFromTestFile(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve caller path")
	}

	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
