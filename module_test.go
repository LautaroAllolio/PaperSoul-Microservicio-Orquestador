package papersoul_test

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"testing"
)

const (
	wantModule    = "github.com/papersoul/orchestrator"
	minGoMajor    = 1
	minGoMinor    = 24
)

func TestGoModModulePath(t *testing.T) {
	line, ok := goModLine(t, "module")
	if !ok {
		t.Fatalf("go.mod: no se encontró la directiva module")
	}
	if got := strings.TrimSpace(strings.TrimPrefix(line, "module")); got != wantModule {
		t.Fatalf("module = %q, quiero %q", got, wantModule)
	}
}

func TestGoModGoVersionAtLeast124(t *testing.T) {
	line, ok := goModLine(t, "go")
	if !ok {
		t.Fatalf("go.mod: no se encontró la directiva go")
	}
	var major, minor int
	if _, err := fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(line, "go")), "%d.%d", &major, &minor); err != nil {
		t.Fatalf("directiva go no es un semver mayor.menor: %q: %v", line, err)
	}
	if major < minGoMajor || (major == minGoMajor && minor < minGoMinor) {
		t.Fatalf("go.mod declara go %d.%d; el plan exige >= %d.%d", major, minor, minGoMajor, minGoMinor)
	}
}

// goModLine devuelve la línea cuya primera palabra es key (ej. "module", "go").
func goModLine(t *testing.T, key string) (string, bool) {
	t.Helper()
	f, err := os.Open("go.mod")
	if err != nil {
		t.Fatalf("no se puede abrir go.mod: %v", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == key {
			return sc.Text(), true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("leyendo go.mod: %v", err)
	}
	return "", false
}