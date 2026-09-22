package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/papersoul/orchestrator/internal/platform/config"
)

const (
	extractorURL   = "http://extractor:8080"
	persistenceURL = "http://persistence:8080"
)

var envVars = []string{
	"ORCH_ADDR",
	"ORCH_MAX_FILE_SIZE",
	"ORCH_MAX_BODY_BYTES",
	"ORCH_VALIDATION_RELAXED",
	"ORCH_MAX_CONCURRENCY",
	"EXTRACTOR_URL",
	"EXTRACTOR_TIMEOUT",
	"PERSISTENCE_URL",
	"PERSISTENCE_TIMEOUT",
	"ORCH_SHUTDOWN_TIMEOUT",
}

func TestLoadAppliesDefaults(t *testing.T) {
	setEnv(t, map[string]string{
		"EXTRACTOR_URL":   extractorURL,
		"PERSISTENCE_URL": persistenceURL,
	})

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load con envs mínimos falló: %v", err)
	}

	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, quiero %q", cfg.Addr, ":8080")
	}
	if cfg.MaxFileSize != 25*1024*1024 {
		t.Errorf("MaxFileSize = %d, quiero %d", cfg.MaxFileSize, 25*1024*1024)
	}
	if cfg.MaxBodyBytes != 25*1024*1024+65536 {
		t.Errorf("MaxBodyBytes = %d, quiero %d (derivado)", cfg.MaxBodyBytes, 25*1024*1024+65536)
	}
	if !cfg.ValidationRelaxed {
		t.Errorf("ValidationRelaxed = false, quiero true")
	}
	if cfg.MaxConcurrency != 8 {
		t.Errorf("MaxConcurrency = %d, quiero %d", cfg.MaxConcurrency, 8)
	}
	if cfg.ExtractorTimeout != 30*time.Second {
		t.Errorf("ExtractorTimeout = %s, quiero %s", cfg.ExtractorTimeout, 30*time.Second)
	}
	if cfg.PersistenceTimeout != 15*time.Second {
		t.Errorf("PersistenceTimeout = %s, quiero %s", cfg.PersistenceTimeout, 15*time.Second)
	}
	if cfg.ShutdownTimeout != 10*time.Second {
		t.Errorf("ShutdownTimeout = %s, quiero %s", cfg.ShutdownTimeout, 10*time.Second)
	}
}

func TestLoadParsesAllVariables(t *testing.T) {
	setEnv(t, map[string]string{
		"ORCH_ADDR":               ":9090",
		"ORCH_MAX_FILE_SIZE":      "10485760",
		"ORCH_MAX_BODY_BYTES":     "120",
		"ORCH_VALIDATION_RELAXED": "false",
		"ORCH_MAX_CONCURRENCY":    "4",
		"EXTRACTOR_URL":           extractorURL,
		"EXTRACTOR_TIMEOUT":       "5s",
		"PERSISTENCE_URL":         persistenceURL,
		"PERSISTENCE_TIMEOUT":     "6s",
		"ORCH_SHUTDOWN_TIMEOUT":   "7s",
	})

	want := config.Config{
		Addr:               ":9090",
		MaxFileSize:        10485760,
		MaxBodyBytes:       120,
		ValidationRelaxed:  false,
		MaxConcurrency:     4,
		ExtractorURL:       extractorURL,
		ExtractorTimeout:   5 * time.Second,
		PersistenceURL:     persistenceURL,
		PersistenceTimeout: 6 * time.Second,
		ShutdownTimeout:    7 * time.Second,
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load falló: %v", err)
	}
	if cfg != want {
		t.Fatalf("Load() = %+v, quiero %+v", cfg, want)
	}
}

func TestMaxBodyBytesDerivedFromMaxFileSize(t *testing.T) {
	setEnv(t, map[string]string{
		"ORCH_MAX_FILE_SIZE": "10485760",
		"EXTRACTOR_URL":      extractorURL,
		"PERSISTENCE_URL":    persistenceURL,
	})

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load falló: %v", err)
	}
	if want := int64(10485760 + 65536); cfg.MaxBodyBytes != want {
		t.Fatalf("MaxBodyBytes = %d, quiero %d", cfg.MaxBodyBytes, want)
	}
}

func TestFailsOnEmptyDownstreamURL(t *testing.T) {
	setEnv(t, map[string]string{
		"PERSISTENCE_URL": persistenceURL,
	})
	if _, err := config.Load(); err == nil {
		t.Fatalf("EXTRACTOR_URL vacía debe fallar")
	} else if !strings.Contains(err.Error(), "EXTRACTOR_URL") {
		t.Fatalf("error (%v) no menciona EXTRACTOR_URL", err)
	}

	setEnv(t, map[string]string{
		"EXTRACTOR_URL": extractorURL,
	})
	if _, err := config.Load(); err == nil {
		t.Fatalf("PERSISTENCE_URL vacía debe fallar")
	} else if !strings.Contains(err.Error(), "PERSISTENCE_URL") {
		t.Fatalf("error (%v) no menciona PERSISTENCE_URL", err)
	}
}

func TestAggregatesInvalidVariableErrors(t *testing.T) {
	setEnv(t, map[string]string{
		"ORCH_MAX_FILE_SIZE": "not-a-number",
		"EXTRACTOR_TIMEOUT":  "forever",
		"EXTRACTOR_URL":      extractorURL,
		"PERSISTENCE_URL":    persistenceURL,
	})

	_, err := config.Load()
	if err == nil {
		t.Fatalf("valores inválidos deben fallar")
	}
	for _, want := range []string{"ORCH_MAX_FILE_SIZE", "EXTRACTOR_TIMEOUT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error (%v) no menciona %s", err, want)
		}
	}
}

func setEnv(t *testing.T, vals map[string]string) {
	t.Helper()
	for _, k := range envVars {
		t.Setenv(k, vals[k])
	}
}
