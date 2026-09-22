package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/papersoul/orchestrator/internal/domain"
	"github.com/papersoul/orchestrator/internal/handler"
	"github.com/papersoul/orchestrator/internal/platform/config"
	"github.com/papersoul/orchestrator/internal/platform/problem"
)

const testCorrelationID = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"

type fakeService struct {
	input *domain.ProcessInput
	res   *domain.ProcessResult
	err   error
	calls int
}

func (f *fakeService) Process(ctx context.Context, in *domain.ProcessInput) (*domain.ProcessResult, error) {
	f.calls++
	f.input = in
	return f.res, f.err
}

func testConfig() config.Config {
	return config.Config{
		Addr:              ":0",
		MaxFileSize:       1024,
		MaxBodyBytes:      4096,
		ValidationRelaxed: true,
		MaxConcurrency:    1,
	}
}

func validPdfBytes(n int) []byte {
	return append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte("x"), n)...)
}

func newRequest(t *testing.T, withFile bool, fileContent []byte, extra map[string]string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for name, value := range extra {
		if err := mw.WriteField(name, value); err != nil {
			t.Fatalf("escribiendo field %q: %v", name, err)
		}
	}
	if withFile {
		w, err := mw.CreateFormFile("file", "doc.pdf")
		if err != nil {
			t.Fatalf("creando file part: %v", err)
		}
		if _, err := w.Write(fileContent); err != nil {
			t.Fatalf("escribiendo file part: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("cerrando multipart: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/documents/process", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Correlation-Id", testCorrelationID)
	return req
}

func doRequest(t *testing.T, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	fake := &fakeService{res: &domain.ProcessResult{DocumentID: "x", Status: domain.StatusProcessed, Checksum: "y"}}
	h := handler.NewProcessHandler(fake, testConfig())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	return rr
}

func decodeProblem(t *testing.T, rr *httptest.ResponseRecorder) problem.Problem {
	t.Helper()
	var p problem.Problem
	if err := json.NewDecoder(rr.Body).Decode(&p); err != nil {
		t.Fatalf("decodificando problem+json (body %q): %v", rr.Body.String(), err)
	}
	return p
}

func TestHandleProcessValidMultipart(t *testing.T) {
	content := validPdfBytes(64)
	fake := &fakeService{}
	fake.res = &domain.ProcessResult{
		DocumentID: "a1b2c3d4",
		Status:     domain.StatusProcessed,
		Checksum:   "abc123",
		FileName:   "doc.pdf",
		Size:       int64(len(content)),
		PageCount:  2,
		Encrypted:  false,
	}
	h := handler.NewProcessHandler(fake, testConfig())

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, newRequest(t, true, content, nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, quiero 200 (body %q)", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, quiero application/json", ct)
	}
	var got struct {
		DocumentID string `json:"documentId"`
		Status     string `json:"status"`
		Checksum   string `json:"checksum"`
		Metadata   struct {
			FileName  string `json:"fileName"`
			SizeBytes int64  `json:"sizeBytes"`
			PageCount int    `json:"pageCount"`
			Encrypted bool   `json:"encrypted"`
		} `json:"metadata"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decodificando response: %v", err)
	}
	if got.DocumentID != fake.res.DocumentID || got.Status != string(fake.res.Status) ||
		got.Checksum != fake.res.Checksum || got.Metadata.FileName != fake.res.FileName ||
		got.Metadata.SizeBytes != fake.res.Size || got.Metadata.PageCount != fake.res.PageCount ||
		got.Metadata.Encrypted != fake.res.Encrypted {
		t.Fatalf("response inesperado: %+v", got)
	}

	if fake.calls != 1 {
		t.Fatalf("calls = %d, quiero 1", fake.calls)
	}
	in := fake.input
	if in == nil {
		t.Fatal("service no recibió ProcessInput")
	}
	if in.FileName != "doc.pdf" {
		t.Fatalf("FileName = %q, quiero doc.pdf", in.FileName)
	}
	if in.Size != int64(len(content)) {
		t.Fatalf("Size = %d, quiero %d", in.Size, len(content))
	}
	br, ok := in.File.(*bytes.Reader)
	if !ok {
		t.Fatalf("File = %T, quiero *bytes.Reader", in.File)
	}
	gotBytes, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("leyendo File: %v", err)
	}
	if !bytes.Equal(gotBytes, content) {
		t.Fatalf("contenido del buffer difiere")
	}
	if _, err := in.File.Seek(0, io.SeekCurrent); err != nil {
		t.Fatalf("File debería ser seekeable: %v", err)
	}
}

func TestHandleMissingFilePartReturns400(t *testing.T) {
	rr := doRequest(t, newRequest(t, false, nil, map[string]string{"foo": "bar"}))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, quiero 400 (body %q)", rr.Code, rr.Body.String())
	}
	p := decodeProblem(t, rr)
	if p.Type != problem.TypeInvalidMultipart || p.Status != http.StatusBadRequest {
		t.Fatalf("problem inesperado: %+v", p)
	}
	if p.Instance != "urn:uuid:"+testCorrelationID {
		t.Fatalf("instance = %q", p.Instance)
	}
	if len(p.InvalidParams) != 1 || p.InvalidParams[0].Name != "file" || p.InvalidParams[0].Reason != "required" {
		t.Fatalf("invalid_params = %+v, quiero [{file required}]", p.InvalidParams)
	}
}

func TestHandleBadMagicBytesReturns422(t *testing.T) {
	rr := doRequest(t, newRequest(t, true, []byte("GIF89a-not-a-pdf-0000000000"), nil))

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, quiero 422 (body %q)", rr.Code, rr.Body.String())
	}
	p := decodeProblem(t, rr)
	if p.Type != problem.TypeInvalidPDFHeader || p.Status != http.StatusUnprocessableEntity {
		t.Fatalf("problem inesperado: %+v", p)
	}
	if len(p.InvalidParams) != 1 || p.InvalidParams[0].Name != "file" || p.InvalidParams[0].Reason != "invalid_magic_bytes" {
		t.Fatalf("invalid_params = %+v, quiero [{file invalid_magic_bytes}]", p.InvalidParams)
	}
}

func TestHandleBadMagicBytesBiggerThanLimitReturns422Not413(t *testing.T) {
	rr := doRequest(t, newRequest(t, true, []byte("GIF89a"+strings.Repeat("g", 4096)), nil))

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, quiero 422 (abort early en magic bytes, body %q)", rr.Code, rr.Body.String())
	}
}

func TestHandleFileOverLimitReturns413(t *testing.T) {
	rr := doRequest(t, newRequest(t, true, validPdfBytes(4096), nil))

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, quiero 413 (body %q)", rr.Code, rr.Body.String())
	}
	p := decodeProblem(t, rr)
	if p.Type != problem.TypeFileTooLarge || p.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("problem inesperado: %+v", p)
	}
	if len(p.InvalidParams) != 1 || p.InvalidParams[0].Name != "file" || p.InvalidParams[0].Reason != "max_size_exceeded" {
		t.Fatalf("invalid_params = %+v, quiero [{file max_size_exceeded}]", p.InvalidParams)
	}
}

func TestHandleBodyOverLimitReturns413(t *testing.T) {
	extra := map[string]string{"padding": strings.Repeat("z", 16*1024)}
	rr := doRequest(t, newRequest(t, true, validPdfBytes(64), extra))

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, quiero 413 (body %q)", rr.Code, rr.Body.String())
	}
	p := decodeProblem(t, rr)
	if p.Type != problem.TypeFileTooLarge || p.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("problem inesperado: %+v", p)
	}
}

func TestHandleMalformedMultipartReturns400(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/documents/process", strings.NewReader("esto no es multipart"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=noexiste")
	req.Header.Set("X-Correlation-Id", testCorrelationID)

	rr := doRequest(t, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, quiero 400 (body %q)", rr.Code, rr.Body.String())
	}
	p := decodeProblem(t, rr)
	if p.Type != problem.TypeInvalidMultipart || p.Status != http.StatusBadRequest {
		t.Fatalf("problem inesperado: %+v", p)
	}
}

func TestHandleEmptyFilePartReturns422(t *testing.T) {
	rr := doRequest(t, newRequest(t, true, nil, nil))

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, quiero 422 (body %q)", rr.Code, rr.Body.String())
	}
	p := decodeProblem(t, rr)
	if p.Type != problem.TypeInvalidPDFHeader || p.Status != http.StatusUnprocessableEntity {
		t.Fatalf("problem inesperado: %+v", p)
	}
}
