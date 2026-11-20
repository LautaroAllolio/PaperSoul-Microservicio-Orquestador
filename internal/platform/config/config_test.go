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
	"ORCH_PUBLIC_HOST",
	"ORCH_TRAEFIK_ENTRYPOINT",
	"ORCH_TRAEFIK_ROUTER",
	"ORCH_TRAEFIK_RETRY_ATTEMPTS",
	"ORCH_TRAEFIK_RETRY_INTERVAL",
	"ORCH_TRAEFIK_HEALTH_INTERVAL",
	"ORCH_TRAEFIK_HEALTH_TIMEOUT",
	"ORCH_CONSUL_CHECK_INTERVAL",
	"ORCH_CONSUL_CHECK_TIMEOUT",
	"ORCH_CONSUL_DEREGISTER_AFTER",
	"ORCH_DEREGISTER_TIMEOUT",
	"ORCH_READ_HEADER_TIMEOUT",
	"ORCH_IDLE_TIMEOUT",
	"ORCH_RETRY_AFTER",
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

	if cfg.PublicHost != "orquestador.universidad.localhost" {
		t.Errorf("PublicHost = %q, quiero %q", cfg.PublicHost, "orquestador.universidad.localhost")
	}
	if cfg.TraefikEntrypoint != "https" {
		t.Errorf("TraefikEntrypoint = %q, quiero %q", cfg.TraefikEntrypoint, "https")
	}
	if cfg.TraefikRouter != "orchestrator" {
		t.Errorf("TraefikRouter = %q, quiero %q", cfg.TraefikRouter, "orchestrator")
	}
	if cfg.TraefikRetryAttempts != 2 {
		t.Errorf("TraefikRetryAttempts = %d, quiero 2", cfg.TraefikRetryAttempts)
	}
	if cfg.TraefikRetryInterval != 500*time.Millisecond {
		t.Errorf("TraefikRetryInterval = %s, quiero 500ms", cfg.TraefikRetryInterval)
	}
	if cfg.TraefikHealthInterval != 10*time.Second {
		t.Errorf("TraefikHealthInterval = %s, quiero 10s", cfg.TraefikHealthInterval)
	}
	if cfg.TraefikHealthTimeout != 3*time.Second {
		t.Errorf("TraefikHealthTimeout = %s, quiero 3s", cfg.TraefikHealthTimeout)
	}
	if cfg.ConsulCheckInterval != 3*time.Second {
		t.Errorf("ConsulCheckInterval = %s, quiero 3s", cfg.ConsulCheckInterval)
	}
	if cfg.ConsulCheckTimeout != time.Second {
		t.Errorf("ConsulCheckTimeout = %s, quiero 1s", cfg.ConsulCheckTimeout)
	}
	if cfg.ConsulDeregisterAfter != time.Minute {
		t.Errorf("ConsulDeregisterAfter = %s, quiero 1m", cfg.ConsulDeregisterAfter)
	}
	if cfg.DeregisterTimeout != 2*time.Second {
		t.Errorf("DeregisterTimeout = %s, quiero 2s", cfg.DeregisterTimeout)
	}
	if cfg.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("ReadHeaderTimeout = %s, quiero 5s", cfg.ReadHeaderTimeout)
	}
	if cfg.IdleTimeout != 60*time.Second {
		t.Errorf("IdleTimeout = %s, quiero 60s", cfg.IdleTimeout)
	}
	if cfg.RetryAfter != 1 {
		t.Errorf("RetryAfter = %d, quiero 1", cfg.RetryAfter)
	}
}

func TestLoadParsesAllVariables(t *testing.T) {
	setEnv(t, map[string]string{
		"ORCH_ADDR":                    ":9090",
		"ORCH_MAX_FILE_SIZE":           "10485760",
		"ORCH_MAX_BODY_BYTES":          "120",
		"ORCH_VALIDATION_RELAXED":      "false",
		"ORCH_MAX_CONCURRENCY":         "4",
		"EXTRACTOR_URL":                extractorURL,
		"EXTRACTOR_TIMEOUT":            "5s",
		"PERSISTENCE_URL":              persistenceURL,
		"PERSISTENCE_TIMEOUT":          "6s",
		"ORCH_SHUTDOWN_TIMEOUT":        "7s",
		"DISCOVERY":                    "consul",
		"CONSUL_HTTP_ADDR":             "http://consul:8500",
		"ORCH_ADVERTISE_ADDR":          "172.18.0.5",
		"ORCH_PUBLIC_HOST":             "docs.mi-universidad.localhost",
		"ORCH_TRAEFIK_ENTRYPOINT":      "websecure",
		"ORCH_TRAEFIK_ROUTER":          "custom-router",
		"ORCH_TRAEFIK_RETRY_ATTEMPTS":  "5",
		"ORCH_TRAEFIK_RETRY_INTERVAL":  "750ms",
		"ORCH_TRAEFIK_HEALTH_INTERVAL": "20s",
		"ORCH_TRAEFIK_HEALTH_TIMEOUT":  "4s",
		"ORCH_CONSUL_CHECK_INTERVAL":   "9s",
		"ORCH_CONSUL_CHECK_TIMEOUT":    "2s",
		"ORCH_CONSUL_DEREGISTER_AFTER": "5m",
		"ORCH_DEREGISTER_TIMEOUT":      "8s",
		"ORCH_READ_HEADER_TIMEOUT":     "11s",
		"ORCH_IDLE_TIMEOUT":            "120s",
		"ORCH_RETRY_AFTER":             "7",
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

		PublicHost:            "docs.mi-universidad.localhost",
		TraefikEntrypoint:     "websecure",
		TraefikRouter:         "custom-router",
		TraefikRetryAttempts:  5,
		TraefikRetryInterval:  750 * time.Millisecond,
		TraefikHealthInterval: 20 * time.Second,
		TraefikHealthTimeout:  4 * time.Second,
		ConsulCheckInterval:   9 * time.Second,
		ConsulCheckTimeout:    2 * time.Second,
		ConsulDeregisterAfter: 5 * time.Minute,
		DeregisterTimeout:     8 * time.Second,
		ReadHeaderTimeout:     11 * time.Second,
		IdleTimeout:           120 * time.Second,
		RetryAfter:            7,
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

func TestFailsOnBlankRequiredStrings(t *testing.T) {
	for _, key := range []string{"ORCH_PUBLIC_HOST", "ORCH_TRAEFIK_ENTRYPOINT", "ORCH_TRAEFIK_ROUTER"} {
		t.Run(key, func(t *testing.T) {
			setEnv(t, map[string]string{
				key:               "   ",
				"EXTRACTOR_URL":   extractorURL,
				"PERSISTENCE_URL": persistenceURL,
			})
			_, err := config.Load()
			if err == nil {
				t.Fatalf("%s en blanco debe fallar", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("error (%v) no menciona %s", err, key)
			}
		})
	}
}

func TestFailsOnNonPositiveDurations(t *testing.T) {
	keys := []string{
		"ORCH_TRAEFIK_RETRY_INTERVAL",
		"ORCH_TRAEFIK_HEALTH_INTERVAL",
		"ORCH_TRAEFIK_HEALTH_TIMEOUT",
		"ORCH_CONSUL_CHECK_INTERVAL",
		"ORCH_CONSUL_CHECK_TIMEOUT",
		"ORCH_CONSUL_DEREGISTER_AFTER",
		"ORCH_DEREGISTER_TIMEOUT",
		"ORCH_READ_HEADER_TIMEOUT",
		"ORCH_IDLE_TIMEOUT",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			setEnv(t, map[string]string{
				key:               "0s",
				"EXTRACTOR_URL":   extractorURL,
				"PERSISTENCE_URL": persistenceURL,
			})
			_, err := config.Load()
			if err == nil {
				t.Fatalf("%s = 0s debe fallar", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("error (%v) no menciona %s", err, key)
			}
		})
	}
}

func TestFailsOnNonPositiveIntegers(t *testing.T) {
	for _, key := range []string{"ORCH_TRAEFIK_RETRY_ATTEMPTS", "ORCH_RETRY_AFTER"} {
		t.Run(key, func(t *testing.T) {
			setEnv(t, map[string]string{
				key:               "0",
				"EXTRACTOR_URL":   extractorURL,
				"PERSISTENCE_URL": persistenceURL,
			})
			_, err := config.Load()
			if err == nil {
				t.Fatalf("%s = 0 debe fallar", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("error (%v) no menciona %s", err, key)
			}
		})
	}
}

func TestFailsOnMalformedNewVariables(t *testing.T) {
	setEnv(t, map[string]string{
		"ORCH_TRAEFIK_RETRY_ATTEMPTS": "many",
		"ORCH_TRAEFIK_HEALTH_TIMEOUT": "forever",
		"EXTRACTOR_URL":               extractorURL,
		"PERSISTENCE_URL":             persistenceURL,
	})

	_, err := config.Load()
	if err == nil {
		t.Fatalf("valores malformados deben fallar")
	}
	for _, want := range []string{"ORCH_TRAEFIK_RETRY_ATTEMPTS", "ORCH_TRAEFIK_HEALTH_TIMEOUT"} {
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
