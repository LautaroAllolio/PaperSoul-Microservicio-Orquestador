package client_test

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/papersoul/orchestrator/internal/client"
	"github.com/papersoul/orchestrator/internal/domain"
	"github.com/papersoul/orchestrator/internal/platform/reqid"
)

const (
	testCorrelationID = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	testChecksum      = "5f4dcc3b5aa765d61d8327deb882cf99b2fd96a382d4c0a3f51d9c5f1d8f0a1b"
)

// Los contracts del plan (sección 2.3) congelados como interfaces anónimas: si
// la firma de un client cambia, el assert de abajo deja de compilar.
var (
	_ interface {
		Extract(context.Context, domain.ExtractRequest) (*domain.ExtractResponse, error)
	} = (*client.Extractor)(nil)

	_ interface {
		FindByChecksum(context.Context, string) (*domain.StoredDocumentResponse, error)
		Store(context.Context, domain.StoreDocumentRequest) (*domain.StoredDocumentResponse, error)
	} = (*client.Persistence)(nil)
)

// requestCapture es lo que un fake downstream observa de la petición.
type requestCapture struct {
	Method           string
	Path             string
	ContentType      string
	ContentLength    int64
	BodyLength       int64
	TransferEncoding []string
	ChecksumHeader   string
	ChecksumField    string
	CorrelationID    string
	CorrelationSet   bool
	FileName         string
	FileContent      []byte
	Body             []byte
}

func pdfBytes(n int) []byte {
	return append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte("x"), n)...)
}

func newExtractRequest(fileName string, content []byte) domain.ExtractRequest {
	return domain.ExtractRequest{
		File:     bytes.NewReader(content),
		FileName: fileName,
		Checksum: testChecksum,
		Size:     int64(len(content)),
	}
}

// newFakeServer levanta un downstream que captura la request completa y responde
// con el status/cuerpo indicados.
func newFakeServer(t *testing.T, got *requestCapture, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("el fake no pudo leer el body: %v", err)
		}
		got.Method = r.Method
		got.Path = r.URL.Path
		got.ContentType = r.Header.Get("Content-Type")
		got.ContentLength = r.ContentLength
		got.BodyLength = int64(len(raw))
		got.TransferEncoding = r.TransferEncoding
		got.ChecksumHeader = r.Header.Get("X-Document-Checksum")
		got.CorrelationID = r.Header.Get("X-Correlation-Id")
		_, got.CorrelationSet = r.Header["X-Correlation-Id"]
		got.Body = raw
		if strings.HasPrefix(got.ContentType, "multipart/form-data") {
			got.ChecksumField, got.FileName, got.FileContent = parseMultipart(t, got.ContentType, raw)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// parseMultipart recorre el body recibido y extrae el campo checksum y la parte file.
func parseMultipart(t *testing.T, contentType string, raw []byte) (checksumField, fileName string, file []byte) {
	t.Helper()
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("Content-Type %q no parseable: %v", contentType, err)
	}
	boundary, ok := params["boundary"]
	if !ok {
		t.Fatalf("Content-Type %q sin boundary", contentType)
	}
	mr := multipart.NewReader(bytes.NewReader(raw), boundary)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return checksumField, fileName, file
		}
		if err != nil {
			t.Fatalf("recorriendo partes multipart: %v", err)
		}
		data, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("leyendo la parte %q: %v", part.FormName(), err)
		}
		switch part.FormName() {
		case "checksum":
			checksumField = string(data)
		case "file":
			fileName = part.FileName()
			file = data
		}
	}
}

// newSlowServer levanta un downstream que lee el body y después duerme más que
// el timeout del client, para forzar el deadline.
func newSlowServer(t *testing.T, sleep time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(sleep)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func ctxWithCorrelation() context.Context {
	return reqid.With(context.Background(), testCorrelationID)
}
