package problem_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/papersoul/orchestrator/internal/platform/problem"
)

func TestProblemSerializesAllFields(t *testing.T) {
	p := problem.Problem{
		Type:          problem.TypeInvalidPDFHeader,
		Title:         "El archivo no es un PDF válido",
		Status:        http.StatusUnprocessableEntity,
		Detail:        "detalle de la ocurrencia",
		Instance:      "urn:uuid:corr-123",
		InvalidParams: []problem.InvalidParam{{Name: "file", Reason: "invalid_magic_bytes"}},
	}

	want := `{"type":"urn:papersoul:orchestrator:invalid-pdf-header","title":"El archivo no es un PDF válido","status":422,"detail":"detalle de la ocurrencia","instance":"urn:uuid:corr-123","invalid_params":[{"name":"file","reason":"invalid_magic_bytes"}]}`
	if got := marshal(t, p); got != want {
		t.Fatalf("JSON de Problem incorrecto:\n got=%s\nwant=%s", got, want)
	}
}

func TestProblemOmitsEmptyOptionalFields(t *testing.T) {
	p := problem.Problem{
		Type:   problem.TypeInternalError,
		Title:  "Error interno del servidor",
		Status: http.StatusInternalServerError,
	}

	want := `{"type":"urn:papersoul:orchestrator:internal-error","title":"Error interno del servidor","status":500}`
	if got := marshal(t, p); got != want {
		t.Fatalf("JSON de Problem incorrecto:\n got=%s\nwant=%s", got, want)
	}
}

func TestProblemOmitsEmptyInvalidParamsSlice(t *testing.T) {
	p := problem.Problem{
		Type:          problem.TypeInvalidMultipart,
		Title:         "Petición inválida",
		Status:        http.StatusBadRequest,
		InvalidParams: []problem.InvalidParam{},
	}

	want := `{"type":"urn:papersoul:orchestrator:invalid-multipart","title":"Petición inválida","status":400}`
	if got := marshal(t, p); got != want {
		t.Fatalf("invalid_params vacío debe omitirse:\n got=%s\nwant=%s", got, want)
	}
}

func TestWriteSetsContentTypeStatusAndBody(t *testing.T) {
	p := problem.Problem{
		Type:          problem.TypeFileTooLarge,
		Title:         "Archivo demasiado grande",
		Status:        http.StatusRequestEntityTooLarge,
		Detail:        "El archivo excede el límite de 25 MiB",
		InvalidParams: []problem.InvalidParam{{Name: "file", Reason: "max_size_exceeded"}},
	}

	w := httptest.NewRecorder()
	problem.Write(w, &http.Request{}, p)
	res := w.Result()
	defer res.Body.Close()

	if ct := res.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, quiero %q", ct, "application/problem+json")
	}
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, quiero %d", res.StatusCode, http.StatusRequestEntityTooLarge)
	}

	var got problem.Problem
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("body no decodifica como Problem: %v", err)
	}
	if got.Type != p.Type || got.Title != p.Title || got.Status != p.Status || got.Detail != p.Detail {
		t.Fatalf("body = %+v, quiero %+v", got, p)
	}
	if len(got.InvalidParams) != 1 || got.InvalidParams[0] != p.InvalidParams[0] {
		t.Fatalf("invalid_params = %+v, quiero %+v", got.InvalidParams, p.InvalidParams)
	}
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
