package errors

import (
	"errors"
	"net/http"

	"github.com/papersoul/orchestrator/internal/platform/problem"
)

type problemSpec struct {
	sentinel error
	typ      string
	title    string
	status   int
	params   []problem.InvalidParam
}

var table = []problemSpec{
	{ErrInvalidMultipart, problem.TypeInvalidMultipart, "Petición inválida", http.StatusBadRequest, nil},
	{ErrMissingFilePart, problem.TypeInvalidMultipart, "Petición inválida", http.StatusBadRequest, []problem.InvalidParam{{Name: "file", Reason: "required"}}},
	{ErrFileTooLarge, problem.TypeFileTooLarge, "Archivo demasiado grande", http.StatusRequestEntityTooLarge, []problem.InvalidParam{{Name: "file", Reason: "max_size_exceeded"}}},
	{ErrInvalidPDFHeader, problem.TypeInvalidPDFHeader, "El archivo no es un PDF válido", http.StatusUnprocessableEntity, []problem.InvalidParam{{Name: "file", Reason: "invalid_magic_bytes"}}},
	{ErrPDFCorrupted, problem.TypeInvalidPDF, "El documento PDF está corrupto", http.StatusUnprocessableEntity, nil},
	{ErrPDFEncrypted, problem.TypeEncryptedPDF, "El documento PDF está cifrado", http.StatusUnprocessableEntity, nil},
	{ErrExtractorUnavailable, problem.TypeExtractorUnavailable, "El servicio de extracción no está disponible", http.StatusBadGateway, nil},
	{ErrExtractorTimeout, problem.TypeDownstreamTimeout, "Tiempo de espera agotado", http.StatusGatewayTimeout, nil},
	{ErrExtractorInvalidResponse, problem.TypeExtractorUnavailable, "El servicio de extracción devolvió una respuesta inválida", http.StatusBadGateway, nil},
	{ErrPersistenceUnavailable, problem.TypePersistenceUnavailable, "El servicio de persistencia no está disponible", http.StatusBadGateway, nil},
	{ErrPersistenceTimeout, problem.TypeDownstreamTimeout, "Tiempo de espera agotado", http.StatusGatewayTimeout, nil},
	{ErrInternal, problem.TypeInternalError, "Error interno del servidor", http.StatusInternalServerError, nil},
}

func Map(r *http.Request, err error) problem.Problem {
	for _, spec := range table {
		if errors.Is(err, spec.sentinel) {
			return problem.Problem{
				Type:          spec.typ,
				Title:         spec.title,
				Status:        spec.status,
				Instance:      instance(r),
				InvalidParams: spec.params,
			}
		}
	}
	return problem.Problem{
		Type:     problem.TypeInternalError,
		Title:    "Error interno del servidor",
		Status:   http.StatusInternalServerError,
		Instance: instance(r),
	}
}

func instance(r *http.Request) string {
	return "urn:uuid:" + r.Header.Get("X-Correlation-Id")
}
