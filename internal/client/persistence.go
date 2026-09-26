package client

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/papersoul/orchestrator/internal/domain"
	errorsvc "github.com/papersoul/orchestrator/internal/platform/errors"
)

const (
	// documentsPath es el endpoint de alta de documentos de Persistencia.
	documentsPath = "/api/v1/documents"
	// byChecksumPath es el endpoint de dedup: GET /documents/by-checksum/{sha256}.
	byChecksumPath = documentsPath + "/by-checksum/"
)

// Persistence es el adapter HTTP hacia el microservicio de Persistencia.
type Persistence struct {
	baseURL string
	http    *http.Client
}

// NewPersistence arma el client de Persistencia.
func NewPersistence(opts Options) *Persistence {
	return &Persistence{baseURL: baseURL(opts.BaseURL), http: newHTTPClient(opts.Timeout)}
}

// FindByChecksum busca un documento por el SHA-256 de su binario. Es el primer
// paso del dedup: devuelve ErrDocumentNotFound cuando el checksum es nuevo, un
// sentinel de control de flujo que el orquestador nunca expone al cliente.
func (p *Persistence) FindByChecksum(ctx context.Context, checksum string) (*domain.StoredDocumentResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+byChecksumPath+url.PathEscape(checksum), nil)
	if err != nil {
		return nil, internalError(err)
	}
	setCommonHeaders(req, ctx)

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, transportError(errorsvc.ErrPersistenceTimeout, errorsvc.ErrPersistenceUnavailable, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		drainAndClose(resp)
		return nil, errorsvc.ErrDocumentNotFound
	}
	if !is2xx(resp.StatusCode) {
		return nil, downstreamStatusError(errorsvc.ErrPersistenceUnavailable, resp)
	}

	var out domain.StoredDocumentResponse
	if err := decodeJSON(resp, &out, errorsvc.ErrPersistenceUnavailable); err != nil {
		return nil, err
	}
	return &out, nil
}

// Store persiste el resultado de la extracción. El body es el JSON plano de
// domain.StoreDocumentRequest (nada de binario): el orquestador ya calculó
// pdf_hash, text_hash y uploaded_at.
//
// Un 409 significa que otro request insertó el mismo checksum primero: se
// devuelve ErrPersistenceConflict y el orquestador relee y responde REUSED.
func (p *Persistence) Store(ctx context.Context, in domain.StoreDocumentRequest) (*domain.StoredDocumentResponse, error) {
	payload, err := json.Marshal(in)
	if err != nil {
		return nil, internalError(err)
	}

	// bytes.Reader ⇒ http.NewRequest calcula ContentLength solo (sin chunked).
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+documentsPath, bytes.NewReader(payload))
	if err != nil {
		return nil, internalError(err)
	}
	req.Header.Set("Content-Type", "application/json")
	setCommonHeaders(req, ctx)

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, transportError(errorsvc.ErrPersistenceTimeout, errorsvc.ErrPersistenceUnavailable, err)
	}
	if resp.StatusCode == http.StatusConflict {
		drainAndClose(resp)
		return nil, errorsvc.ErrPersistenceConflict
	}
	if !is2xx(resp.StatusCode) {
		return nil, downstreamStatusError(errorsvc.ErrPersistenceUnavailable, resp)
	}

	var out domain.StoredDocumentResponse
	if err := decodeJSON(resp, &out, errorsvc.ErrPersistenceUnavailable); err != nil {
		return nil, err
	}
	return &out, nil
}
