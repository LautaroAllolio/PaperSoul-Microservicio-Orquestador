package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/papersoul/orchestrator/internal/domain"
	errorsvc "github.com/papersoul/orchestrator/internal/platform/errors"
)

// ExtractorClient es el contrato del microservicio de Extracción.
type ExtractorClient interface {
	Extract(ctx context.Context, in domain.ExtractRequest) (*domain.ExtractResponse, error)
}

// PersistenceClient es el contrato del microservicio de Persistencia.
// FindByChecksum retorna ErrDocumentNotFound cuando el checksum no existe.
type PersistenceClient interface {
	FindByChecksum(ctx context.Context, checksum string) (*domain.StoredDocumentResponse, error)
	Store(ctx context.Context, in domain.StoreDocumentRequest) (*domain.StoredDocumentResponse, error)
}

// PDFValidator valida estructura y cifrado del PDF en memoria.
type PDFValidator interface {
	Validate(r io.ReadSeeker) (pageCount int, err error)
}

// Orchestrator es el dueño del flujo de proceso. No tiene estado: cada request
// es dueño de su buffer y los clients son seguros para usar en paralelo.
type Orchestrator struct {
	extractor   ExtractorClient
	persistence PersistenceClient
	validator   PDFValidator
}

// NewOrchestrator cablea las tres collaboratoras del flujo.
func NewOrchestrator(extractor ExtractorClient, persistence PersistenceClient, validator PDFValidator) *Orchestrator {
	return &Orchestrator{extractor: extractor, persistence: persistence, validator: validator}
}

// Process orquesta checksum → dedup → validación → extracción → persistencia.
// Toda entrada de tipo sentinel que sale de las capas inferiores se propaga
// intacta: el mapper de RFC 9457 la traduce a su status.
func (o *Orchestrator) Process(ctx context.Context, in *domain.ProcessInput) (*domain.ProcessResult, error) {
	if in == nil || in.File == nil {
		return nil, errorsvc.ErrInternal
	}

	checksum, err := checksumSHA256(in.File) // deja el reader al inicio
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errorsvc.ErrInternal, err)
	}

	// Dedup antes de gastar extracción: el checksum es la clave de idempotencia.
	stored, err := o.persistence.FindByChecksum(ctx, checksum)
	switch {
	case err == nil:
		return resultFrom(stored, domain.StatusReused, checksum, in), nil
	case !errors.Is(err, errorsvc.ErrDocumentNotFound):
		return nil, err // ya es un sentinel mapeable (502/504)
	}

	// Checksum nuevo: guardamos la estructura antes de llamar al extractor.
	// El pageCount del validador se descarta a propósito: la fuente del contador
	// es el extractor (ver TestElPageCountDelExtractorMandaSobreElDelValidador).
	if _, err := o.validator.Validate(in.File); err != nil {
		return nil, err // ErrPDFCorrupted / ErrPDFEncrypted → 422; ErrInternal → 500
	}

	// El orquestador es el dueño del buffer: no confiamos en que la colaborador
	// anterior lo haya dejado al principio.
	if _, err := in.File.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("%w: %v", errorsvc.ErrInternal, err)
	}

	ext, err := o.extractor.Extract(ctx, domain.ExtractRequest{
		File:     in.File,
		FileName: in.FileName,
		Checksum: checksum,
		Size:     in.Size,
	})
	if err != nil {
		return nil, err
	}
	// Defensa en profundidad: no persistimos un resultado incoherente aunque el
	// extractor haya devuelto algo fuera de contrato.
	if ext == nil {
		return nil, fmt.Errorf("%w: el extractor no devolvió respuesta", errorsvc.ErrExtractorInvalidResponse)
	}
	if err := ext.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", errorsvc.ErrExtractorInvalidResponse, err)
	}

	// El orquestador calcula el hash del texto y la fecha de subida.
	textHash := sha256.Sum256([]byte(ext.ExtractedText))

	stored, err = o.persistence.Store(ctx, domain.StoreDocumentRequest{
		FileName:         in.FileName,
		ExtractedText:    ext.ExtractedText,
		ExtractionMethod: ext.ExtractionMethod,
		PageCount:        ext.PageCount,
		PDFHash:          checksum,
		TextHash:         hex.EncodeToString(textHash[:]),
		UploadedAt:       time.Now().UTC(),
	})
	if err != nil {
		if !errors.Is(err, errorsvc.ErrPersistenceConflict) {
			return nil, err
		}
		// Carrera de dedup: otro request insertó el mismo checksum primero.
		// Re-leemos el resultado persistido y lo reusamos.
		stored, err = o.persistence.FindByChecksum(ctx, checksum)
		if err != nil {
			return nil, err
		}
		return resultFrom(stored, domain.StatusReused, checksum, in), nil
	}

	return resultFrom(stored, domain.StatusProcessed, checksum, in), nil
}

// resultFrom mapea la respuesta de Persistencia al 200 del orquestador, que no
// incluye el texto extraído crudo.
func resultFrom(stored *domain.StoredDocumentResponse, status domain.ProcessStatus, checksum string, in *domain.ProcessInput) *domain.ProcessResult {
	return &domain.ProcessResult{
		DocumentID: stored.ID,
		Status:     status,
		Checksum:   checksum, // = stored.PDFHash: ambos son el SHA-256 del binario
		FileName:   stored.FileName,
		Size:       in.Size,
		PageCount:  stored.PageCount,
		Encrypted:  false, // los PDFs cifrados se rechazan con 422 antes de llegar acá
	}
}

// checksumSHA256 hashea el binario y deja el reader al principio: el mismo reader
// se reutiliza para validar y reenviar durante todo el request.
func checksumSHA256(r io.ReadSeeker) (string, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
