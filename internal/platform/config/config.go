package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

const (
	defaultAddr               = ":8080"
	defaultMaxFileSize        = 25 << 20
	defaultBodyOverhead       = 65536
	defaultValidationRelaxed  = true
	defaultMaxConcurrency     = 8
	defaultExtractorTimeout   = 30 * time.Second
	defaultPersistenceTimeout = 15 * time.Second
	defaultShutdownTimeout    = 10 * time.Second
)

type Config struct {
	Addr               string
	MaxFileSize        int64
	MaxBodyBytes       int64
	ValidationRelaxed  bool
	MaxConcurrency     int
	ExtractorURL       string
	ExtractorTimeout   time.Duration
	PersistenceURL     string
	PersistenceTimeout time.Duration
	ShutdownTimeout    time.Duration
}

func Load() (Config, error) {
	cfg := Config{Addr: envOr("ORCH_ADDR", defaultAddr)}

	var errs []error

	maxFileSize, err := parseInt64("ORCH_MAX_FILE_SIZE", defaultMaxFileSize)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.MaxFileSize = maxFileSize

	maxBodyBytes, err := parseInt64("ORCH_MAX_BODY_BYTES", maxFileSize+defaultBodyOverhead)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.MaxBodyBytes = maxBodyBytes

	relaxed, err := parseBool("ORCH_VALIDATION_RELAXED", defaultValidationRelaxed)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.ValidationRelaxed = relaxed

	maxConcurrency, err := parseInt("ORCH_MAX_CONCURRENCY", defaultMaxConcurrency)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.MaxConcurrency = maxConcurrency

	extractorURL := envOr("EXTRACTOR_URL", "")
	if extractorURL == "" {
		errs = append(errs, requiredError("EXTRACTOR_URL"))
	}
	cfg.ExtractorURL = extractorURL

	extractorTimeout, err := parseDuration("EXTRACTOR_TIMEOUT", defaultExtractorTimeout)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.ExtractorTimeout = extractorTimeout

	persistenceURL := envOr("PERSISTENCE_URL", "")
	if persistenceURL == "" {
		errs = append(errs, requiredError("PERSISTENCE_URL"))
	}
	cfg.PersistenceURL = persistenceURL

	persistenceTimeout, err := parseDuration("PERSISTENCE_TIMEOUT", defaultPersistenceTimeout)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.PersistenceTimeout = persistenceTimeout

	shutdownTimeout, err := parseDuration("ORCH_SHUTDOWN_TIMEOUT", defaultShutdownTimeout)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.ShutdownTimeout = shutdownTimeout

	if cfg.MaxFileSize <= 0 {
		errs = append(errs, fmt.Errorf("ORCH_MAX_FILE_SIZE debe ser > 0, got %d", cfg.MaxFileSize))
	}
	if cfg.MaxConcurrency <= 0 {
		errs = append(errs, fmt.Errorf("ORCH_MAX_CONCURRENCY debe ser > 0, got %d", cfg.MaxConcurrency))
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseInt64(key string, fallback int64) (int64, error) {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fallback, fmt.Errorf("%s: valor inválido %q: %w", key, v, err)
		}
		return n, nil
	}
	return fallback, nil
}

func parseInt(key string, fallback int) (int, error) {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fallback, fmt.Errorf("%s: valor inválido %q: %w", key, v, err)
		}
		return n, nil
	}
	return fallback, nil
}

func parseBool(key string, fallback bool) (bool, error) {
	if v := os.Getenv(key); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fallback, fmt.Errorf("%s: valor inválido %q: %w", key, v, err)
		}
		return b, nil
	}
	return fallback, nil
}

func parseDuration(key string, fallback time.Duration) (time.Duration, error) {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fallback, fmt.Errorf("%s: valor inválido %q: %w", key, v, err)
		}
		return d, nil
	}
	return fallback, nil
}

func requiredError(key string) error {
	return fmt.Errorf("%s: variable requerida y vacía", key)
}
