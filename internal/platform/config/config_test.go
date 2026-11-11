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
	"DISCOVERY",
	"CONSUL_HTTP_ADDR",
	"ORCH_ADVERTISE_ADDR",
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
	if cfg.Discovery != "static" {
		t.Errorf("Discovery = %q, quiero %q", cfg.Discovery, "static")
	}
	if cfg.ConsulHTTPAddr != "" {
		t.Errorf("ConsulHTTPAddr = %q, quiero vacío", cfg.ConsulHTTPAddr)
	}
	if cfg.AdvertiseAddr != "" {
		t.Errorf("AdvertiseAddr = %q, quiero vacío", cfg.AdvertiseAddr)
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
		"DISCOVERY":               "consul",
		"CONSUL_HTTP_ADDR":        "http://consul:8500",
		"ORCH_ADVERTISE_ADDR":     "172.18.0.5",
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
		Discovery:          "consul",
		ConsulHTTPAddr:     "http://consul:8500",
		AdvertiseAddr:      "172.18.0.5",
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

func TestFailsOnInvalidDiscoveryMode(t *testing.T) {
	setEnv(t, map[string]string{
		"DISCOVERY":       "dns",
		"EXTRACTOR_URL":   extractorURL,
		"PERSISTENCE_URL": persistenceURL,
	})

	_, err := config.Load()
	if err == nil {
		t.Fatalf("DISCOVERY inválido debe fallar")
	}
	if !strings.Contains(err.Error(), "DISCOVERY") {
		t.Fatalf("error (%v) no menciona DISCOVERY", err)
	}
}

func TestConsulModeNoRequiereURLsFijas(t *testing.T) {
	setEnv(t, map[string]string{
		"DISCOVERY":        "consul",
		"CONSUL_HTTP_ADDR": "http://consul:8500",
	})

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("modo consul sin URLs fijas debe arrancar: %v", err)
	}
	if cfg.ExtractorURL != "" || cfg.PersistenceURL != "" {
		t.Fatalf("en modo consul las URLs fijas deben quedar vacías: %+v", cfg)
	}
}

func TestConsulModeRequiereConsulHTTPAddr(t *testing.T) {
	setEnv(t, map[string]string{"DISCOVERY": "consul"})

	_, err := config.Load()
	if err == nil {
		t.Fatalf("modo consul sin CONSUL_HTTP_ADDR debe fallar")
	}
	if !strings.Contains(err.Error(), "CONSUL_HTTP_ADDR") {
		t.Fatalf("error (%v) no menciona CONSUL_HTTP_ADDR", err)
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
