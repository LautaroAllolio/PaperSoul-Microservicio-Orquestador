package errors_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	errorsvc "github.com/papersoul/orchestrator/internal/platform/errors"
	"github.com/papersoul/orchestrator/internal/platform/problem"
)

type mapperCase struct {
	name   string
	err    error
	status int
	typ    string
	title  string
	params []problem.InvalidParam
}

func TestMapCoversErrorMatrix(t *testing.T) {
	cases := []mapperCase{
		{"ErrInvalidMultipart", errorsvc.ErrInvalidMultipart, 400, problem.TypeInvalidMultipart, "Petición inválida", nil},
		{"ErrMissingFilePart", errorsvc.ErrMissingFilePart, 400, problem.TypeInvalidMultipart, "Petición inválida", []problem.InvalidParam{{Name: "file", Reason: "required"}}},
		{"ErrFileTooLarge", errorsvc.ErrFileTooLarge, 413, problem.TypeFileTooLarge, "Archivo demasiado grande", []problem.InvalidParam{{Name: "file", Reason: "max_size_exceeded"}}},
		{"ErrInvalidPDFHeader", errorsvc.ErrInvalidPDFHeader, 422, problem.TypeInvalidPDFHeader, "El archivo no es un PDF válido", []problem.InvalidParam{{Name: "file", Reason: "invalid_magic_bytes"}}},
		{"ErrPDFCorrupted", errorsvc.ErrPDFCorrupted, 422, problem.TypeInvalidPDF, "El documento PDF está corrupto", nil},
		{"ErrPDFEncrypted", errorsvc.ErrPDFEncrypted, 422, problem.TypeEncryptedPDF, "El documento PDF está cifrado", nil},
		{"ErrExtractorUnavailable", errorsvc.ErrExtractorUnavailable, 502, problem.TypeExtractorUnavailable, "El servicio de extracción no está disponible", nil},
		{"ErrExtractorTimeout", errorsvc.ErrExtractorTimeout, 504, problem.TypeDownstreamTimeout, "Tiempo de espera agotado", nil},
		{"ErrExtractorInvalidResponse", errorsvc.ErrExtractorInvalidResponse, 502, problem.TypeExtractorUnavailable, "El servicio de extracción devolvió una respuesta inválida", nil},
		{"ErrPersistenceUnavailable", errorsvc.ErrPersistenceUnavailable, 502, problem.TypePersistenceUnavailable, "El servicio de persistencia no está disponible", nil},
		{"ErrPersistenceTimeout", errorsvc.ErrPersistenceTimeout, 504, problem.TypeDownstreamTimeout, "Tiempo de espera agotado", nil},
		{"ErrInternal", errorsvc.ErrInternal, 500, problem.TypeInternalError, "Error interno del servidor", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := errorsvc.Map(requestWithCorrelation("corr-123"), tc.err)

			if p.Status != tc.status {
				t.Errorf("status = %d, quiero %d", p.Status, tc.status)
			}
			if p.Type != tc.typ {
				t.Errorf("type = %q, quiero %q", p.Type, tc.typ)
			}
			if p.Title != tc.title {
				t.Errorf("title = %q, quiero %q", p.Title, tc.title)
			}
			if p.Instance != "urn:uuid:corr-123" {
				t.Errorf("instance = %q, quiero %q", p.Instance, "urn:uuid:corr-123")
			}
			if p.Detail != "" {
				t.Errorf("detail = %q, debe estar vacío (no filtrar internos)", p.Detail)
			}
			if !reflect.DeepEqual(p.InvalidParams, tc.params) {
				t.Errorf("invalid_params = %+v, quiero %+v", p.InvalidParams, tc.params)
			}
		})
	}
}

func TestMapResolvesWrappedSentinels(t *testing.T) {
	wrapped := fmt.Errorf("otra capa: %w", errorsvc.ErrPDFCorrupted)
	p := errorsvc.Map(requestWithCorrelation("c"), wrapped)
	if p.Status != 422 || p.Type != problem.TypeInvalidPDF {
		t.Fatalf("errores envueltos deben mapearse por errors.Is: status=%d type=%q", p.Status, p.Type)
	}
}

func TestMapNeverExposesControlFlowSentinels(t *testing.T) {
	for name, err := range map[string]error{
		"ErrDocumentNotFound":    errorsvc.ErrDocumentNotFound,
		"ErrPersistenceConflict": errorsvc.ErrPersistenceConflict,
	} {
		t.Run(name, func(t *testing.T) {
			p := errorsvc.Map(requestWithCorrelation("c"), err)
			if p.Status != 500 || p.Type != problem.TypeInternalError {
				t.Fatalf("sentinel de control de flujo expuesto: status=%d type=%q", p.Status, p.Type)
			}
		})
	}
}

func TestMapUnknownErrorBecomesInternal(t *testing.T) {
	var boom = errors.New("fallo no contemplado")
	p := errorsvc.Map(requestWithCorrelation("c"), boom)
	if p.Status != 500 || p.Type != problem.TypeInternalError {
		t.Fatalf("error desconocido = status %d type %q, quiero 500/internal-error", p.Status, p.Type)
	}
	if p.Detail != "" {
		t.Fatalf("detail = %q, no debe filtrar el error interno", p.Detail)
	}
}

func requestWithCorrelation(corr string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/documents/process", nil)
	r.Header.Set("X-Correlation-Id", corr)
	return r
}
