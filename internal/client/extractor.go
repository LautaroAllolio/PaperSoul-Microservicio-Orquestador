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
	resolver Resolver
	http     *http.Client
}

// NewExtractor arma el client del Extractor.
func NewExtractor(opts Options) *Extractor {
	return &Extractor{resolver: opts.Resolver, http: newHTTPClient(opts.Timeout)}
}

// Extract reenvía el binario —que ya está en memoria— al Extractor por
// multipart en streaming y decodifica la respuesta tipada, validando el esquema
// plano: extraction_method ∈ {pymupdf, ocr} y page_count >= 1.
func (e *Extractor) Extract(ctx context.Context, in domain.ExtractRequest) (*domain.ExtractResponse, error) {
	if in.File == nil {
		return nil, errorsvc.ErrInternal
	}

	resp, err := sendResolved(ctx, e.resolver, ServiceExtractor, e.http,
		errorsvc.ErrExtractorTimeout, errorsvc.ErrExtractorUnavailable,
		func(base string) (*http.Request, error) {
			return newExtractHTTPRequest(ctx, base, in)
		})
	if err != nil {
		return nil, err
	}
	if !is2xx(resp.StatusCode) {
		return nil, downstreamStatusError(extractStatusSentinel(resp.StatusCode), resp)
	}

	var out domain.ExtractResponse
	if err := decodeJSON(resp, &out, errorsvc.ErrExtractorInvalidResponse); err != nil {
		return nil, err
	}
	// El invariante vive en domain: lo comparte el orquestador antes de persistir.
	if err := out.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", errorsvc.ErrExtractorInvalidResponse, err)
	}
	return &out, nil
}

// newExtractHTTPRequest arma el request multipart. Es una función aparte porque
// el reintento por dial necesita reconstruirlo completo: el pipe anterior ya se
// consumió y el reader del binario quedó avanzado.
func newExtractHTTPRequest(ctx context.Context, base string, in domain.ExtractRequest) (*http.Request, error) {
	// El reader es de un solo dueño por request, pero el checksum y la validación
	// pdfcpu ya lo recorrieron: siempre arrancamos desde el principio.
	if _, err := in.File.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	body, contentType, totalLen, err := NewMultipartPipe(in.File, in.FileName, in.Checksum, in.Size)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+extractionsPath, body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = totalLen // evita transfer-chunked
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(headerDocumentChecksum, in.Checksum)
	setCommonHeaders(req, ctx)
	return req, nil
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
