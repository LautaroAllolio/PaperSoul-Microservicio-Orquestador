package papersoul_test

import (
	"os"
	"path/filepath"
	"testing"
)

var requiredDirs = []string{
	"cmd/orchestrator",
	"internal/handler",
	"internal/service",
	"internal/client",
	"internal/domain",
	"internal/platform",
}

func TestLayerDirectoriesExist(t *testing.T) {
	for _, dir := range requiredDirs {
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("falta el directorio %q: %v", dir, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%q existe pero no es un directorio", dir)
		}
	}
}

func TestLayerDirectoriesHaveGoFiles(t *testing.T) {
	for _, dir := range requiredDirs {
		matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Errorf("glob en %q: %v", dir, err)
			continue
		}
		if len(matches) == 0 {
			t.Errorf("el directorio %q no contiene archivos .go", dir)
		}
	}
}
