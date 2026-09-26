package service_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/papersoul/orchestrator/internal/domain"
	errorsvc "github.com/papersoul/orchestrator/internal/platform/errors"
	"github.com/papersoul/orchestrator/internal/platform/pdf"
	"github.com/papersoul/orchestrator/internal/service"
)

// El validador real de pdfcpu debe satisfacer el contrato del orquestador.
var _ service.PDFValidator = (*pdf.Validator)(nil)

const (
	fileName     = "factura.pdf"
	storedDocID  = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	extractedTxt = "texto extraído"
)

// fakeExtractor mimetiza al client real: re-sekea a 0 y consume el binario entero,
// así el test puede verificar que el orquestador no le entrega un reader desfasado.
type fakeExtractor struct {
	calls int
	read  []byte
	got   domain.ExtractRequest
	res   *domain.ExtractResponse
	err   error
}

func (f *fakeExtractor) Extract(_ context.Context, in domain.ExtractRequest) (*domain.ExtractResponse, error) {
	f.calls++
	f.got = in
	if in.File != nil {
		if _, err := in.File.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		read, err := io.ReadAll(in.File)
		if err != nil {
			return nil, err
		}
		f.read = read
	}
	return f.res, f.err
}

// fakePersistence con funciones inyectables: por defecto FindByChecksum dice
// "checksum nuevo" y Store devuelve el documento reflejando lo recibido.
type fakePersistence struct {
	findCalls  int
	findGot    []string
	findFn     func(call int, checksum string) (*domain.StoredDocumentResponse, error)
	storeCalls int
	storeGot   []domain.StoreDocumentRequest
	storeFn    func(call int, in domain.StoreDocumentRequest) (*domain.StoredDocumentResponse, error)
}

func (f *fakePersistence) FindByChecksum(_ context.Context, checksum string) (*domain.StoredDocumentResponse, error) {
	f.findCalls++
	f.findGot = append(f.findGot, checksum)
	if f.findFn != nil {
		return f.findFn(f.findCalls, checksum)
	}
	return nil, errorsvc.ErrDocumentNotFound
}

func (f *fakePersistence) Store(_ context.Context, in domain.StoreDocumentRequest) (*domain.StoredDocumentResponse, error) {
	f.storeCalls++
	f.storeGot = append(f.storeGot, in)
	if f.storeFn != nil {
		return f.storeFn(f.storeCalls, in)
	}
	return &domain.StoredDocumentResponse{
		ID:        storedDocID,
		Status:    "indexed",
		PDFHash:   in.PDFHash,
		FileName:  in.FileName,
		PageCount: in.PageCount,
	}, nil
}

type fakeValidator struct {
	calls int
	read  []byte
	pages int
	err   error
}

func (f *fakeValidator) Validate(r io.ReadSeeker) (int, error) {
	f.calls++
	if r != nil {
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return 0, err
		}
		read, err := io.ReadAll(r)
		if err != nil {
			return 0, err
		}
		f.read = read
	}
	if f.err != nil {
		return 0, f.err
	}
	return f.pages, nil
}

type fixture struct {
	orch        *service.Orchestrator
	extractor   *fakeExtractor
	persistence *fakePersistence
	validator   *fakeValidator
}

func newFixture() *fixture {
	ext := &fakeExtractor{}
	pers := &fakePersistence{}
	val := &fakeValidator{}
	return &fixture{
		orch:        service.NewOrchestrator(ext, pers, val),
		extractor:   ext,
		persistence: pers,
		validator:   val,
	}
}

func pdfBytes(n int) []byte {
	return append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte("x"), n)...)
}

func newInput(content []byte) *domain.ProcessInput {
	return &domain.ProcessInput{
		FileName: fileName,
		Size:     int64(len(content)),
		File:     bytes.NewReader(content),
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func storedDocWithHash(sum string) *domain.StoredDocumentResponse {
	return &domain.StoredDocumentResponse{
		ID:        storedDocID,
		Status:    "indexed",
		PDFHash:   sum,
		FileName:  fileName,
		PageCount: 2,
	}
}

func validExtractResponse() *domain.ExtractResponse {
	return &domain.ExtractResponse{
		ExtractedText:    extractedTxt,
		ExtractionMethod: domain.ExtractionMethodPyMuPDF,
		PageCount:        7,
	}
}

// assertCalls falla si el orquestador abrió más llamadas de las esperadas: el
// dedup precede a la validación y ninguna capa llama a la siguiente dos veces.
func assertCalls(t *testing.T, f *fixture, find, validate, extract, store int) {
	t.Helper()
	if f.persistence.findCalls != find || f.validator.calls != validate ||
		f.extractor.calls != extract || f.persistence.storeCalls != store {
		t.Fatalf("llamadas find/validate/extract/store = %d/%d/%d/%d, quiero %d/%d/%d/%d",
			f.persistence.findCalls, f.validator.calls, f.extractor.calls, f.persistence.storeCalls,
			find, validate, extract, store)
	}
}

func TestDedupHitRespondeReusedSinInvocarElExtractor(t *testing.T) {
	content := pdfBytes(256)
	sum := sha256Hex(content)
	doc := storedDocWithHash(sum)

	f := newFixture()
	f.persistence.findFn = func(_ int, _ string) (*domain.StoredDocumentResponse, error) { return doc, nil }

	res, err := f.orch.Process(context.Background(), newInput(content))
	if err != nil {
		t.Fatalf("Process = %v, quiero nil", err)
	}
	if res.Status != domain.StatusReused {
		t.Fatalf("status = %q, quiero REUSED", res.Status)
	}
	if res.DocumentID != doc.ID || res.Checksum != sum || res.Checksum != doc.PDFHash ||
		res.FileName != doc.FileName || res.PageCount != doc.PageCount ||
		res.Size != int64(len(content)) || res.Encrypted {
		t.Fatalf("result inesperado: %+v", res)
	}
	assertCalls(t, f, 1, 0, 0, 0)
	t.Logf("manual check ⇒ extractor.calls=%d validator.calls=%d storeCalls=%d status=%s",
		f.extractor.calls, f.validator.calls, f.persistence.storeCalls, res.Status)
}

func TestProcesoCompletoPersisteElResultado(t *testing.T) {
	content := pdfBytes(512)
	sum := sha256Hex(content)
	wantTextHash := sha256Hex([]byte(extractedTxt))

	f := newFixture()
	f.validator.pages = 2
	f.extractor.res = validExtractResponse()

	before := time.Now().UTC()
	res, err := f.orch.Process(context.Background(), newInput(content))
	after := time.Now().UTC()
	if err != nil {
		t.Fatalf("Process = %v, quiero nil", err)
	}
	assertCalls(t, f, 1, 1, 1, 1)

	if res.Status != domain.StatusProcessed {
		t.Fatalf("status = %q, quiero PROCESSED", res.Status)
	}
	if res.DocumentID != storedDocID || res.Checksum != sum ||
		res.FileName != fileName || res.PageCount != 7 || res.Size != int64(len(content)) || res.Encrypted {
		t.Fatalf("result inesperado: %+v", res)
	}

	got := f.persistence.storeGot[0]
	if got.FileName != fileName || got.ExtractedText != extractedTxt ||
		got.ExtractionMethod != domain.ExtractionMethodPyMuPDF || got.PageCount != 7 {
		t.Fatalf("StoreDocumentRequest inesperado: %+v", got)
	}
	if got.PDFHash != sum {
		t.Fatalf("PDFHash = %q, quiero el checksum del binario %q", got.PDFHash, sum)
	}
	if got.TextHash != wantTextHash {
		t.Fatalf("TextHash = %q, quiero sha256(%q) = %q", got.TextHash, extractedTxt, wantTextHash)
	}
	if got.UploadedAt.Location().String() != "UTC" {
		t.Fatalf("UploadedAt.Location() = %q, quiero UTC", got.UploadedAt.Location())
	}
	if got.UploadedAt.Before(before) || got.UploadedAt.After(after) {
		t.Fatalf("UploadedAt = %v, quiero un instante dentro de [%v, %v]", got.UploadedAt, before, after)
	}
}

func TestElPageCountDelExtractorMandaSobreElDelValidador(t *testing.T) {
	content := pdfBytes(256)
	f := newFixture()
	f.validator.pages = 99 // el validador es guardia de estructura, no contador
	f.extractor.res = validExtractResponse()

	res, err := f.orch.Process(context.Background(), newInput(content))
	if err != nil {
		t.Fatalf("Process = %v, quiero nil", err)
	}
	if res.PageCount != 7 {
		t.Fatalf("PageCount = %d, quiero 7 (el contador autoritativo es el del extractor)", res.PageCount)
	}
	if got := f.persistence.storeGot[0].PageCount; got != 7 {
		t.Fatalf("StoreDocumentRequest.PageCount = %d, quiero 7", got)
	}
}

func TestElChecksumEsElSHA256DelBinarioYViajaAlExtractor(t *testing.T) {
	content := pdfBytes(1024)
	sum := sha256Hex(content)
	f := newFixture()
	f.extractor.res = validExtractResponse()

	res, err := f.orch.Process(context.Background(), newInput(content))
	if err != nil {
		t.Fatalf("Process = %v, quiero nil", err)
	}
	if f.persistence.findGot[0] != sum {
		t.Fatalf("FindByChecksum recibió %q, quiero el SHA-256 del binario %q", f.persistence.findGot[0], sum)
	}
	if f.extractor.got.Checksum != sum || f.extractor.got.FileName != fileName || f.extractor.got.Size != int64(len(content)) {
		t.Fatalf("ExtractRequest inesperado: %+v", f.extractor.got)
	}
	if res.Checksum != sum {
		t.Fatalf("checksum de la respuesta = %q, quiero %q", res.Checksum, sum)
	}
}

func TestValidateFallaNoInvocaAlExtractorNiAStore(t *testing.T) {
	cases := []struct {
		nombre string
		err    error
	}{
		{"pdf corrupto", errorsvc.ErrPDFCorrupted},
		{"pdf cifrado", errorsvc.ErrPDFEncrypted},
		{"fallo interno del validador", errorsvc.ErrInternal},
	}

	for _, tc := range cases {
		t.Run(tc.nombre, func(t *testing.T) {
			f := newFixture()
			f.validator.err = tc.err
			f.extractor.res = validExtractResponse()

			res, err := f.orch.Process(context.Background(), newInput(pdfBytes(128)))
			if !errors.Is(err, tc.err) {
				t.Fatalf("error = %v, quiero %v", err, tc.err)
			}
			if res != nil {
				t.Fatalf("result = %+v, quiero nil en error", res)
			}
			assertCalls(t, f, 1, 1, 0, 0)
		})
	}
}

func TestExtractInvalidoNoPersiste(t *testing.T) {
	cases := []struct {
		nombre string
		res    *domain.ExtractResponse
	}{
		{"extraction_method desconocido", &domain.ExtractResponse{ExtractionMethod: "magia", PageCount: 1}},
		{"extraction_method ausente", &domain.ExtractResponse{PageCount: 1}},
		{"page_count 0", &domain.ExtractResponse{ExtractionMethod: domain.ExtractionMethodOCR}},
		{"page_count negativo", &domain.ExtractResponse{ExtractionMethod: domain.ExtractionMethodOCR, PageCount: -3}},
		{"respuesta nil sin error", nil},
	}

	for _, tc := range cases {
		t.Run(tc.nombre, func(t *testing.T) {
			f := newFixture()
			f.extractor.res = tc.res

			res, err := f.orch.Process(context.Background(), newInput(pdfBytes(128)))
			if !errors.Is(err, errorsvc.ErrExtractorInvalidResponse) {
				t.Fatalf("error = %v, quiero ErrExtractorInvalidResponse (502)", err)
			}
			if res != nil {
				t.Fatalf("result = %+v, quiero nil en error", res)
			}
			assertCalls(t, f, 1, 1, 1, 0)
		})
	}
}

func TestErroresDelExtractorSePropagan(t *testing.T) {
	for _, wantErr := range []error{
		errorsvc.ErrExtractorUnavailable,
		errorsvc.ErrExtractorTimeout,
		errorsvc.ErrExtractorInvalidResponse,
	} {
		t.Run(wantErr.Error(), func(t *testing.T) {
			f := newFixture()
			f.extractor.err = wantErr

			res, err := f.orch.Process(context.Background(), newInput(pdfBytes(128)))
			if !errors.Is(err, wantErr) {
				t.Fatalf("error = %v, quiero el sentinel %v intacto", err, wantErr)
			}
			if res != nil {
				t.Fatalf("result = %+v, quiero nil en error", res)
			}
			assertCalls(t, f, 1, 1, 1, 0)
		})
	}
}

func TestStore409ReleePorChecksumYRespondeReused(t *testing.T) {
	content := pdfBytes(256)
	sum := sha256Hex(content)
	doc := storedDocWithHash(sum)

	f := newFixture()
	f.extractor.res = validExtractResponse()
	f.persistence.storeFn = func(_ int, _ domain.StoreDocumentRequest) (*domain.StoredDocumentResponse, error) {
		return nil, errorsvc.ErrPersistenceConflict
	}
	f.persistence.findFn = func(call int, _ string) (*domain.StoredDocumentResponse, error) {
		if call == 1 {
			return nil, errorsvc.ErrDocumentNotFound // todavía no existía
		}
		return doc, nil // ganó otro request con el mismo checksum
	}

	res, err := f.orch.Process(context.Background(), newInput(content))
	if err != nil {
		t.Fatalf("Process = %v, quiero nil", err)
	}
	if res.Status != domain.StatusReused {
		t.Fatalf("status = %q, quiero REUSED tras el 409", res.Status)
	}
	if res.DocumentID != doc.ID || res.Checksum != sum {
		t.Fatalf("result inesperado: %+v", res)
	}
	assertCalls(t, f, 2, 1, 1, 1)
	if f.persistence.findGot[0] != sum || f.persistence.findGot[1] != sum {
		t.Fatalf("los dos FindByChecksum deben usar el mismo checksum: %v", f.persistence.findGot)
	}
}

func TestStore409ConRelecturaFallidaPropagaElError(t *testing.T) {
	content := pdfBytes(256)
	f := newFixture()
	f.extractor.res = validExtractResponse()
	f.persistence.storeFn = func(_ int, _ domain.StoreDocumentRequest) (*domain.StoredDocumentResponse, error) {
		return nil, errorsvc.ErrPersistenceConflict
	}
	f.persistence.findFn = func(call int, _ string) (*domain.StoredDocumentResponse, error) {
		if call == 1 {
			return nil, errorsvc.ErrDocumentNotFound
		}
		return nil, errorsvc.ErrPersistenceUnavailable
	}

	res, err := f.orch.Process(context.Background(), newInput(content))
	if !errors.Is(err, errorsvc.ErrPersistenceUnavailable) {
		t.Fatalf("error = %v, quiero ErrPersistenceUnavailable", err)
	}
	if res != nil {
		t.Fatalf("result = %+v, quiero nil en error", res)
	}
	assertCalls(t, f, 2, 1, 1, 1)
}

func TestErroresDeFindByChecksumNoCaenAValidar(t *testing.T) {
	for _, wantErr := range []error{
		errorsvc.ErrPersistenceUnavailable,
		errorsvc.ErrPersistenceTimeout,
	} {
		t.Run(wantErr.Error(), func(t *testing.T) {
			f := newFixture()
			f.persistence.findFn = func(_ int, _ string) (*domain.StoredDocumentResponse, error) {
				return nil, wantErr
			}
			f.extractor.res = validExtractResponse()

			res, err := f.orch.Process(context.Background(), newInput(pdfBytes(128)))
			if !errors.Is(err, wantErr) {
				t.Fatalf("error = %v, quiero el sentinel %v intacto", err, wantErr)
			}
			if res != nil {
				t.Fatalf("result = %+v, quiero nil en error", res)
			}
			assertCalls(t, f, 1, 0, 0, 0)
		})
	}
}

func TestErroresDeStoreSePropagan(t *testing.T) {
	for _, wantErr := range []error{
		errorsvc.ErrPersistenceUnavailable,
		errorsvc.ErrPersistenceTimeout,
	} {
		t.Run(wantErr.Error(), func(t *testing.T) {
			f := newFixture()
			f.extractor.res = validExtractResponse()
			f.persistence.storeFn = func(_ int, _ domain.StoreDocumentRequest) (*domain.StoredDocumentResponse, error) {
				return nil, wantErr
			}

			res, err := f.orch.Process(context.Background(), newInput(pdfBytes(128)))
			if !errors.Is(err, wantErr) {
				t.Fatalf("error = %v, quiero el sentinel %v intacto", err, wantErr)
			}
			if res != nil {
				t.Fatalf("result = %+v, quiero nil en error", res)
			}
			assertCalls(t, f, 1, 1, 1, 1)
		})
	}
}

func TestElReaderSeResekeaAntesDeCadaUso(t *testing.T) {
	content := pdfBytes(4096)
	sum := sha256Hex(content)
	f := newFixture()
	f.extractor.res = validExtractResponse()

	in := newInput(content)
	if _, err := in.File.Seek(int64(len(content)/2), io.SeekStart); err != nil {
		t.Fatalf("desfasando el reader: %v", err)
	}

	if _, err := f.orch.Process(context.Background(), in); err != nil {
		t.Fatalf("Process = %v, quiero nil con un reader desfasado de entrada", err)
	}
	if !bytes.Equal(f.validator.read, content) {
		t.Fatalf("el validador recibió %d bytes, quiero el binario completo (%d)", len(f.validator.read), len(content))
	}
	if !bytes.Equal(f.extractor.read, content) {
		t.Fatalf("el extractor recibió %d bytes, quiero el binario completo (%d)", len(f.extractor.read), len(content))
	}
	if f.persistence.findGot[0] != sum {
		t.Fatalf("el checksum se calculó sobre %q, quiero el SHA-256 del binario completo %q", f.persistence.findGot[0], sum)
	}
}

func TestEntradaInvalidaDevuelveErrInternal(t *testing.T) {
	cases := []struct {
		nombre string
		in     *domain.ProcessInput
	}{
		{"input nil", nil},
		{"File nil", &domain.ProcessInput{FileName: fileName, Size: 128}},
	}

	for _, tc := range cases {
		t.Run(tc.nombre, func(t *testing.T) {
			f := newFixture()
			f.extractor.res = validExtractResponse()

			res, err := f.orch.Process(context.Background(), tc.in)
			if !errors.Is(err, errorsvc.ErrInternal) {
				t.Fatalf("error = %v, quiero ErrInternal", err)
			}
			if res != nil {
				t.Fatalf("result = %+v, quiero nil en error", res)
			}
			assertCalls(t, f, 0, 0, 0, 0)
		})
	}
}
