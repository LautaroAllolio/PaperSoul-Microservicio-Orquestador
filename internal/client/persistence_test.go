package client_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/papersoul/orchestrator/internal/client"
	"github.com/papersoul/orchestrator/internal/domain"
	errorsvc "github.com/papersoul/orchestrator/internal/platform/errors"
)

const storedBody = `{"id":"3f2504e0-4f89-41d3-9a0c-0305e82c3301","status":"indexed","pdf_hash":"` + testChecksum + `","filename":"factura.pdf","page_count":7}`

func storeRequest() domain.StoreDocumentRequest {
	return domain.StoreDocumentRequest{
		FileName:         "factura.pdf",
		ExtractedText:    "texto extraído",
		ExtractionMethod: domain.ExtractionMethodPyMuPDF,
		PageCount:        7,
		PDFHash:          testChecksum,
		TextHash:         "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9",
		UploadedAt:       time.Date(2026, 3, 2, 15, 4, 5, 0, time.UTC),
	}
}

func TestFindByChecksumDevuelveElDocumentoAlmacenado(t *testing.T) {
	var got requestCapture
	srv := newFakeServer(t, &got, http.StatusOK, storedBody)

	pers := client.NewPersistence(client.Options{BaseURL: srv.URL, Timeout: 2 * time.Second})
	doc, err := pers.FindByChecksum(ctxWithCorrelation(), testChecksum)
	if err != nil {
		t.Fatalf("FindByChecksum = %v, quiero nil", err)
	}

	if got.Method != http.MethodGet {
		t.Fatalf("method = %q, quiero GET", got.Method)
	}
	if want := "/api/v1/documents/by-checksum/" + testChecksum; got.Path != want {
		t.Fatalf("path = %q, quiero %q", got.Path, want)
	}
	if !got.CorrelationSet || got.CorrelationID != testCorrelationID {
		t.Fatalf("X-Correlation-Id = %q (presente=%v), quiero %q", got.CorrelationID, got.CorrelationSet, testCorrelationID)
	}

	if doc.ID != "3f2504e0-4f89-41d3-9a0c-0305e82c3301" || doc.Status != "indexed" ||
		doc.PDFHash != testChecksum || doc.FileName != "factura.pdf" || doc.PageCount != 7 {
		t.Fatalf("respuesta tipada inesperada: %+v", doc)
	}
}

func TestFindByChecksum404MapeaADocumentNotFound(t *testing.T) {
	var got requestCapture
	srv := newFakeServer(t, &got, http.StatusNotFound, `{"type":"urn:papersoul:persistence:not-found","status":404}`)

	pers := client.NewPersistence(client.Options{BaseURL: srv.URL, Timeout: 2 * time.Second})
	_, err := pers.FindByChecksum(context.Background(), testChecksum)

	if !errors.Is(err, errorsvc.ErrDocumentNotFound) {
		t.Fatalf("error = %v, quiero ErrDocumentNotFound (control de flujo del dedup)", err)
	}
}

func TestFindByChecksumConFalloDelServidorMapeaAUnavailable(t *testing.T) {
	cases := []struct {
		nombre string
		status int
		body   string
	}{
		{"500", http.StatusInternalServerError, `{"detail":"boom"}`},
		{"503", http.StatusServiceUnavailable, ``},
		{"200 con body no JSON", http.StatusOK, `<html>no soy json</html>`},
		{"200 con body vacío", http.StatusOK, ``},
	}

	for _, tc := range cases {
		t.Run(tc.nombre, func(t *testing.T) {
			var got requestCapture
			srv := newFakeServer(t, &got, tc.status, tc.body)

			pers := client.NewPersistence(client.Options{BaseURL: srv.URL, Timeout: 2 * time.Second})
			_, err := pers.FindByChecksum(context.Background(), testChecksum)

			if !errors.Is(err, errorsvc.ErrPersistenceUnavailable) {
				t.Fatalf("error = %v, quiero ErrPersistenceUnavailable", err)
			}
		})
	}
}

func TestFindByChecksumConTimeoutMapeaAPersistenceTimeout(t *testing.T) {
	srv := newSlowServer(t, 300*time.Millisecond)

	pers := client.NewPersistence(client.Options{BaseURL: srv.URL, Timeout: 30 * time.Millisecond})
	_, err := pers.FindByChecksum(context.Background(), testChecksum)

	if !errors.Is(err, errorsvc.ErrPersistenceTimeout) {
		t.Fatalf("error = %v, quiero ErrPersistenceTimeout", err)
	}
}

func TestStoreEnviaElJSONPlanoDeStoreDocumentRequest(t *testing.T) {
	var got requestCapture
	srv := newFakeServer(t, &got, http.StatusCreated, storedBody)

	pers := client.NewPersistence(client.Options{BaseURL: srv.URL, Timeout: 2 * time.Second})
	in := storeRequest()
	doc, err := pers.Store(ctxWithCorrelation(), in)
	if err != nil {
		t.Fatalf("Store = %v, quiero nil", err)
	}
	if doc.ID != "3f2504e0-4f89-41d3-9a0c-0305e82c3301" || doc.PageCount != 7 {
		t.Fatalf("respuesta tipada inesperada: %+v", doc)
	}

	if got.Method != http.MethodPost {
		t.Fatalf("method = %q, quiero POST", got.Method)
	}
	if got.Path != "/api/v1/documents" {
		t.Fatalf("path = %q, quiero /api/v1/documents", got.Path)
	}
	if got.ContentType != "application/json" {
		t.Fatalf("Content-Type = %q, quiero application/json", got.ContentType)
	}
	if len(got.TransferEncoding) != 0 {
		t.Fatalf("TransferEncoding = %v, quiero request sin transfer-chunked", got.TransferEncoding)
	}
	if got.ContentLength != got.BodyLength || got.ContentLength == 0 {
		t.Fatalf("ContentLength = %d vs body %d, quiero un ContentLength exacto", got.ContentLength, got.BodyLength)
	}
	if !got.CorrelationSet || got.CorrelationID != testCorrelationID {
		t.Fatalf("X-Correlation-Id = %q (presente=%v), quiero %q", got.CorrelationID, got.CorrelationSet, testCorrelationID)
	}

	var payload map[string]any
	if err := json.Unmarshal(got.Body, &payload); err != nil {
		t.Fatalf("el body no es JSON: %v (body %q)", err, got.Body)
	}
	want := map[string]any{
		"filename":          "factura.pdf",
		"extracted_text":    "texto extraído",
		"extraction_method": "pymupdf",
		"page_count":        float64(7),
		"pdf_hash":          testChecksum,
		"text_hash":         "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9",
		"uploaded_at":       "2026-03-02T15:04:05Z",
	}
	if len(payload) != len(want) {
		t.Fatalf("el payload tiene %d campos, quiero exactamente %d: %v", len(payload), len(want), payload)
	}
	for key, wantValue := range want {
		gotValue, ok := payload[key]
		if !ok {
			t.Fatalf("falta el campo %q en el payload: %v", key, payload)
		}
		if gotValue != wantValue {
			t.Fatalf("campo %q = %v (%T), quiero %v (%T)", key, gotValue, gotValue, wantValue, wantValue)
		}
	}
}

func TestStoreNoReenviaElBinario(t *testing.T) {
	var got requestCapture
	srv := newFakeServer(t, &got, http.StatusCreated, storedBody)

	pers := client.NewPersistence(client.Options{BaseURL: srv.URL, Timeout: 2 * time.Second})
	if _, err := pers.Store(context.Background(), storeRequest()); err != nil {
		t.Fatalf("Store = %v, quiero nil", err)
	}
	if bytes.Contains(got.Body, []byte("%PDF-")) {
		t.Fatalf("Store reenvió el binario: Persistencia solo recibe el texto extraído (body %q)", got.Body)
	}
}

func TestStore409MapeaAPersistenceConflict(t *testing.T) {
	var got requestCapture
	srv := newFakeServer(t, &got, http.StatusConflict, `{"detail":"checksum duplicado"}`)

	pers := client.NewPersistence(client.Options{BaseURL: srv.URL, Timeout: 2 * time.Second})
	_, err := pers.Store(context.Background(), storeRequest())

	if !errors.Is(err, errorsvc.ErrPersistenceConflict) {
		t.Fatalf("error = %v, quiero ErrPersistenceConflict (race de dedup)", err)
	}
}

func TestStoreConFalloDelServidorMapeaAUnavailable(t *testing.T) {
	cases := []struct {
		nombre string
		status int
		body   string
	}{
		{"500", http.StatusInternalServerError, `{"detail":"boom"}`},
		{"422", http.StatusUnprocessableEntity, `{"detail":"pdf inválido"}`},
		{"201 con body no JSON", http.StatusCreated, `nada`},
	}

	for _, tc := range cases {
		t.Run(tc.nombre, func(t *testing.T) {
			var got requestCapture
			srv := newFakeServer(t, &got, tc.status, tc.body)

			pers := client.NewPersistence(client.Options{BaseURL: srv.URL, Timeout: 2 * time.Second})
			_, err := pers.Store(context.Background(), storeRequest())

			if !errors.Is(err, errorsvc.ErrPersistenceUnavailable) {
				t.Fatalf("error = %v, quiero ErrPersistenceUnavailable", err)
			}
		})
	}
}

func TestStoreConTimeoutMapeaAPersistenceTimeout(t *testing.T) {
	srv := newSlowServer(t, 300*time.Millisecond)

	pers := client.NewPersistence(client.Options{BaseURL: srv.URL, Timeout: 30 * time.Millisecond})
	_, err := pers.Store(context.Background(), storeRequest())

	if !errors.Is(err, errorsvc.ErrPersistenceTimeout) {
		t.Fatalf("error = %v, quiero ErrPersistenceTimeout", err)
	}
}

func TestStoreSinConexionMapeaAUnavailable(t *testing.T) {
	srv := newFakeServer(t, &requestCapture{}, http.StatusCreated, storedBody)
	url := srv.URL
	srv.Close()

	pers := client.NewPersistence(client.Options{BaseURL: url, Timeout: time.Second})
	_, err := pers.Store(context.Background(), storeRequest())

	if !errors.Is(err, errorsvc.ErrPersistenceUnavailable) {
		t.Fatalf("error = %v, quiero ErrPersistenceUnavailable", err)
	}
}
