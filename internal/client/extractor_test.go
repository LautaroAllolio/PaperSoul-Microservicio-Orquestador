package client_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/papersoul/orchestrator/internal/client"
	"github.com/papersoul/orchestrator/internal/domain"
	errorsvc "github.com/papersoul/orchestrator/internal/platform/errors"
)

const validExtractBody = `{"extracted_text":"texto extraído","extraction_method":"pymupdf","page_count":7}`

func TestExtractEnviaMultipartSinChunkedYConContentLengthExacto(t *testing.T) {
	content := pdfBytes(2048)
	var got requestCapture
	srv := newFakeServer(t, &got, http.StatusOK, validExtractBody)

	ext := client.NewExtractor(client.Options{BaseURL: srv.URL + "/", Timeout: 2 * time.Second})
	res, err := ext.Extract(ctxWithCorrelation(), newExtractRequest("factura.pdf", content))
	if err != nil {
		t.Fatalf("Extract = %v, quiero nil", err)
	}

	if res.ExtractedText != "texto extraído" || res.ExtractionMethod != domain.ExtractionMethodPyMuPDF || res.PageCount != 7 {
		t.Fatalf("respuesta tipada inesperada: %+v", res)
	}

	if got.Method != http.MethodPost {
		t.Fatalf("method = %q, quiero POST", got.Method)
	}
	if got.Path != "/api/v1/extractions" {
		t.Fatalf("path = %q, quiero /api/v1/extractions", got.Path)
	}
	if len(got.TransferEncoding) != 0 {
		t.Fatalf("TransferEncoding = %v, quiero request sin transfer-chunked", got.TransferEncoding)
	}
	if got.ContentLength <= 0 {
		t.Fatalf("ContentLength = %d, quiero > 0", got.ContentLength)
	}
	if got.ContentLength != got.BodyLength {
		t.Fatalf("ContentLength = %d pero el body recibido mide %d: el ContentLength precalculado no es exacto", got.ContentLength, got.BodyLength)
	}
	if ct := got.ContentType; !strings.HasPrefix(ct, "multipart/form-data; boundary=") {
		t.Fatalf("Content-Type = %q, quiero multipart/form-data con boundary", ct)
	}

	if got.FileName != "factura.pdf" {
		t.Fatalf("filename = %q, quiero factura.pdf", got.FileName)
	}
	if !bytes.Equal(got.FileContent, content) {
		t.Fatalf("el binario reenviado difiere del original (%d vs %d bytes)", len(got.FileContent), len(content))
	}
	if got.ChecksumField != testChecksum {
		t.Fatalf("campo checksum = %q, quiero %q", got.ChecksumField, testChecksum)
	}
	if got.ChecksumHeader != testChecksum {
		t.Fatalf("header X-Document-Checksum = %q, quiero %q", got.ChecksumHeader, testChecksum)
	}
	if !got.CorrelationSet || got.CorrelationID != testCorrelationID {
		t.Fatalf("X-Correlation-Id = %q (presente=%v), quiero %q", got.CorrelationID, got.CorrelationSet, testCorrelationID)
	}
}

func TestExtractAceptaTextoVacioYMetodoOCR(t *testing.T) {
	var got requestCapture
	srv := newFakeServer(t, &got, http.StatusOK, `{"extracted_text":"","extraction_method":"ocr","page_count":1}`)

	ext := client.NewExtractor(client.Options{BaseURL: srv.URL, Timeout: 2 * time.Second})
	res, err := ext.Extract(context.Background(), newExtractRequest("escaneado.pdf", pdfBytes(64)))
	if err != nil {
		t.Fatalf("Extract = %v, quiero nil: un PDF escaneado sin texto es un resultado válido", err)
	}
	if res.ExtractedText != "" || res.ExtractionMethod != domain.ExtractionMethodOCR || res.PageCount != 1 {
		t.Fatalf("respuesta inesperada: %+v", res)
	}
}

func TestExtractOmiteCorrelationIDSiNoVieneEnElContexto(t *testing.T) {
	var got requestCapture
	srv := newFakeServer(t, &got, http.StatusOK, validExtractBody)

	ext := client.NewExtractor(client.Options{BaseURL: srv.URL, Timeout: 2 * time.Second})
	if _, err := ext.Extract(context.Background(), newExtractRequest("doc.pdf", pdfBytes(64))); err != nil {
		t.Fatalf("Extract = %v, quiero nil", err)
	}
	if got.CorrelationSet {
		t.Fatalf("el client mandó X-Correlation-Id = %q sin haberlo recibido", got.CorrelationID)
	}
}

func TestExtractResekeaElBinarioAntesDeReenviarlo(t *testing.T) {
	content := pdfBytes(1024)
	var got requestCapture
	srv := newFakeServer(t, &got, http.StatusOK, validExtractBody)

	in := newExtractRequest("doc.pdf", content)
	if _, err := in.File.Seek(512, io.SeekStart); err != nil { // reader desfasado por el llamador
		t.Fatalf("desfasando el reader: %v", err)
	}

	ext := client.NewExtractor(client.Options{BaseURL: srv.URL, Timeout: 2 * time.Second})
	if _, err := ext.Extract(context.Background(), in); err != nil {
		t.Fatalf("Extract = %v, quiero nil", err)
	}
	if !bytes.Equal(got.FileContent, content) {
		t.Fatalf("se reenvió un reader desfasado (%d bytes, esperaba %d)", len(got.FileContent), len(content))
	}
}

func TestExtractConFileNilDevuelveErrInternal(t *testing.T) {
	ext := client.NewExtractor(client.Options{BaseURL: "http://127.0.0.1:1", Timeout: time.Second})

	_, err := ext.Extract(context.Background(), domain.ExtractRequest{FileName: "doc.pdf", Size: 10})

	if !errors.Is(err, errorsvc.ErrInternal) {
		t.Fatalf("error = %v, quiero ErrInternal", err)
	}
}

func TestExtractConSizeInvalidoDevuelveError(t *testing.T) {
	ext := client.NewExtractor(client.Options{BaseURL: "http://127.0.0.1:1", Timeout: time.Second})

	_, err := ext.Extract(context.Background(), domain.ExtractRequest{
		File:     bytes.NewReader(pdfBytes(8)),
		FileName: "doc.pdf",
		Size:     0,
	})

	if err == nil {
		t.Fatal("Extract con Size=0 = nil, quiero error (tamaño inconsistente con el binario)")
	}
}

func TestExtractMapeaFallosDelDownstream(t *testing.T) {
	cases := []struct {
		nombre  string
		status  int
		body    string
		wantErr error
	}{
		{"error 500 del extractor", http.StatusInternalServerError, `{"detail":"boom"}`, errorsvc.ErrExtractorUnavailable},
		{"indisponible 503", http.StatusServiceUnavailable, ``, errorsvc.ErrExtractorUnavailable},
		{"rechazo 400", http.StatusBadRequest, `{"detail":"archivo inválido"}`, errorsvc.ErrExtractorInvalidResponse},
		{"rechazo 404", http.StatusNotFound, ``, errorsvc.ErrExtractorInvalidResponse},
		{"body no JSON", http.StatusOK, `<html>no soy json</html>`, errorsvc.ErrExtractorInvalidResponse},
		{"body vacío", http.StatusOK, ``, errorsvc.ErrExtractorInvalidResponse},
		{"extraction_method desconocido", http.StatusOK, `{"extracted_text":"x","extraction_method":"magia","page_count":1}`, errorsvc.ErrExtractorInvalidResponse},
		{"extraction_method ausente", http.StatusOK, `{"extracted_text":"x","page_count":1}`, errorsvc.ErrExtractorInvalidResponse},
		{"page_count 0", http.StatusOK, `{"extracted_text":"x","extraction_method":"ocr","page_count":0}`, errorsvc.ErrExtractorInvalidResponse},
		{"page_count negativo", http.StatusOK, `{"extracted_text":"x","extraction_method":"ocr","page_count":-3}`, errorsvc.ErrExtractorInvalidResponse},
	}

	for _, tc := range cases {
		t.Run(tc.nombre, func(t *testing.T) {
			var got requestCapture
			srv := newFakeServer(t, &got, tc.status, tc.body)

			ext := client.NewExtractor(client.Options{BaseURL: srv.URL, Timeout: 2 * time.Second})
			_, err := ext.Extract(ctxWithCorrelation(), newExtractRequest("doc.pdf", pdfBytes(128)))

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, quiero %v", err, tc.wantErr)
			}
		})
	}
}

func TestExtractSinConexionMapeaAUnavailable(t *testing.T) {
	srv := newFakeServer(t, &requestCapture{}, http.StatusOK, validExtractBody)
	url := srv.URL
	srv.Close() // nadie escucha en ese puerto

	ext := client.NewExtractor(client.Options{BaseURL: url, Timeout: time.Second})
	_, err := ext.Extract(context.Background(), newExtractRequest("doc.pdf", pdfBytes(64)))

	if !errors.Is(err, errorsvc.ErrExtractorUnavailable) {
		t.Fatalf("error = %v, quiero ErrExtractorUnavailable", err)
	}
}

func TestExtractConTimeoutMapeaAErrExtractorTimeout(t *testing.T) {
	srv := newSlowServer(t, 300*time.Millisecond)

	ext := client.NewExtractor(client.Options{BaseURL: srv.URL, Timeout: 30 * time.Millisecond})
	start := time.Now()
	_, err := ext.Extract(context.Background(), newExtractRequest("doc.pdf", pdfBytes(64)))
	elapsed := time.Since(start)

	if !errors.Is(err, errorsvc.ErrExtractorTimeout) {
		t.Fatalf("error = %v, quiero ErrExtractorTimeout", err)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("Extract tardó %v: el deadline del client no cortó la llamada", elapsed)
	}
}
