package errors_test

import (
	"errors"
	"fmt"
	"testing"

	errorsvc "github.com/papersoul/orchestrator/internal/platform/errors"
)

var sentinels = []error{
	errorsvc.ErrInvalidMultipart,
	errorsvc.ErrMissingFilePart,
	errorsvc.ErrFileTooLarge,
	errorsvc.ErrInvalidPDFHeader,
	errorsvc.ErrPDFCorrupted,
	errorsvc.ErrPDFEncrypted,
	errorsvc.ErrExtractorUnavailable,
	errorsvc.ErrExtractorTimeout,
	errorsvc.ErrExtractorInvalidResponse,
	errorsvc.ErrPersistenceUnavailable,
	errorsvc.ErrPersistenceTimeout,
	errorsvc.ErrDocumentNotFound,
	errorsvc.ErrPersistenceConflict,
	errorsvc.ErrInternal,
}

func TestErrorsIsMatchesWrappedSentinel(t *testing.T) {
	for _, sentinel := range sentinels {
		wrapped := fmt.Errorf("contexto: %w", sentinel)
		if !errors.Is(wrapped, sentinel) {
			t.Errorf("errors.Is(%v, %v) = false tras wrap", wrapped, sentinel)
		}
	}
}

func TestSentinelsAreDistinct(t *testing.T) {
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i == j {
				continue
			}
			if errors.Is(a, b) {
				t.Errorf("errors.Is(%v, %v) = true (sentinel %d == sentinel %d)", a, b, i, j)
			}
		}
	}
}
