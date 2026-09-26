package client

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/papersoul/orchestrator/internal/domain"
	errorsvc "github.com/papersoul/orchestrator/internal/platform/errors"
)

const (
	// extractionsPath es el endpoint del microservicio de Extracción.
	extractionsPath = "/api/v1/extractions"
	// headerDocumentChecksum lleva el SHA-256 del binario en la request.
	headerDocumentChecksum = "X-Document-Checksum"
)

// Extractor es el adapter HTTP hacia el microservicio de Extracción.
type Extractor struct {
	baseURL string
	http    *http.Client
}

// NewExtractor arma el client del Extractor.
func NewExtractor(opts Options) *Extractor {
	return &Extractor{baseURL: baseURL(opts.BaseURL), http: newHTTPClient(opts.Timeout)}
}

// Extract reenvía el binario —que ya está en memoria— al Extractor por
// multipart en streaming y decodifica la respuesta tipada, validando el esquema
// plano: extraction_method ∈ {pymupdf, ocr} y page_count >= 1.
func (e *Extractor) Extract(ctx context.Context, in domain.ExtractRequest) (*domain.ExtractResponse, error) {
	if in.File == nil {
		return nil, errorsvc.ErrInternal
	}
	// El reader es de un solo dueño por request, pero el checksum y la validación
	// pdfcpu ya lo recorrieron: siempre arrancamos desde el principio.
	if _, err := in.File.Seek(0, io.SeekStart); err != nil {
		return nil, internalError(err)
	}

	body, contentType, totalLen, err := NewMultipartPipe(in.File, in.FileName, in.Checksum, in.Size)
	if err != nil {
		return nil, internalError(err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+extractionsPath, body)
	if err != nil {
		return nil, internalError(err)
	}
	req.ContentLength = totalLen // evita transfer-chunked
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(headerDocumentChecksum, in.Checksum)
	setCommonHeaders(req, ctx)

	resp, err := e.http.Do(req)
	if err != nil {
		return nil, transportError(errorsvc.ErrExtractorTimeout, errorsvc.ErrExtractorUnavailable, err)
	}
	if !is2xx(resp.StatusCode) {
		return nil, downstreamStatusError(extractStatusSentinel(resp.StatusCode), resp)
	}

	var out domain.ExtractResponse
	if err := decodeJSON(resp, &out, errorsvc.ErrExtractorInvalidResponse); err != nil {
		return nil, err
	}
	if err := validateExtractResponse(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// extractStatusSentinel decide qué significa el status de un 2xx que no llegó:
// un 5xx es el downstream caído (no disponible), un 4xx es el extractor
// rechazando nuestra request — un incumplimiento de contrato, no una caída.
func extractStatusSentinel(code int) error {
	if code >= http.StatusInternalServerError {
		return errorsvc.ErrExtractorUnavailable
	}
	return errorsvc.ErrExtractorInvalidResponse
}

// validateExtractResponse valida el esquema plano de la respuesta: sin
// pass-through genérico, un campo incoherente se traduce a 502.
func validateExtractResponse(out *domain.ExtractResponse) error {
	switch out.ExtractionMethod {
	case domain.ExtractionMethodPyMuPDF, domain.ExtractionMethodOCR:
	default:
		return fmt.Errorf("%w: extraction_method %q", errorsvc.ErrExtractorInvalidResponse, out.ExtractionMethod)
	}
	if out.PageCount < 1 {
		return fmt.Errorf("%w: page_count %d", errorsvc.ErrExtractorInvalidResponse, out.PageCount)
	}
	return nil
}
