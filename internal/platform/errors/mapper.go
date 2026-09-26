package errors

import (
	"errors"
	"net/http"

	"github.com/papersoul/orchestrator/internal/platform/problem"
	"github.com/papersoul/orchestrator/internal/platform/reqid"
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
	{ErrTooManyRequests, problem.TypeOverloaded, "El servicio está saturado", http.StatusServiceUnavailable, nil},
	{ErrRouteNotFound, problem.TypeNotFound, "El recurso no existe", http.StatusNotFound, nil},
	{ErrMethodNotAllowed, problem.TypeMethodNotAllowed, "Método no permitido", http.StatusMethodNotAllowed, nil},
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

// instance prefiere el contexto: el middleware genera el correlation id cuando el
// cliente no manda ninguno, y leer solo el header dejaría "urn:uuid:" vacío justo
// en el caso más común. Si no hay id, se omite: instance es opcional en RFC 9457
// y un URN vacío es peor que nada.
func instance(r *http.Request) string {
	id := reqid.From(r.Context())
	if id == "" {
		id = r.Header.Get(reqid.Header)
	}
	if id == "" {
		return ""
	}
	return "urn:uuid:" + id
}
