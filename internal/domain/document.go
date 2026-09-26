package domain

import (
	"io"
	"time"
)

// ProcessInput es lo que el handler entrega al orquestador.
// File ES el binario en memoria (bytes.Reader): seekeable para pdfcpu y reenvío.
type ProcessInput struct {
	FileName string
	Size     int64
	File     io.ReadSeeker
}

// ProcessStatus distingue flujo completo vs. dedup hit.
type ProcessStatus string

const (
	StatusProcessed ProcessStatus = "PROCESSED"
	StatusReused    ProcessStatus = "REUSED"
)

// ProcessResult es la respuesta 200: documentId + status + checksum + metadata.
// NO incluye el texto extraído crudo (ancho de banda).
type ProcessResult struct {
	DocumentID string
	Status     ProcessStatus
	Checksum   string
	FileName   string
	Size       int64
	PageCount  int
	Encrypted  bool
}

// ExtractRequest es el contrato consumido por el cliente del Extractor.
// File es el binario en memoria: se reenvía por streaming, sin copia adicional.
type ExtractRequest struct {
	File     io.ReadSeeker
	FileName string
	Checksum string
	Size     int64
}

// ExtractResponse es la respuesta tipada del Extractor (sin pass-through genérico).
type ExtractResponse struct {
	ExtractedText    string `json:"extracted_text"`
	ExtractionMethod string `json:"extraction_method"` // "pymupdf" | "ocr"
	PageCount        int    `json:"page_count"`
}

// Métodos de extracción válidos (validación en el cliente del extractor).
const (
	ExtractionMethodPyMuPDF = "pymupdf"
	ExtractionMethodOCR     = "ocr"
)

// StoreDocumentRequest es el payload tipado hacia Persistencia: esquema plano
// estricto compatible con el modelo PdfDocument de MongoDB (PaperSoul).
// El orquestador calcula PDFHash, TextHash y UploadedAt.
type StoreDocumentRequest struct {
	FileName         string    `json:"filename"`
	ExtractedText    string    `json:"extracted_text"`
	ExtractionMethod string    `json:"extraction_method"`
	PageCount        int       `json:"page_count"`
	PDFHash          string    `json:"pdf_hash"`
	TextHash         string    `json:"text_hash"`
	UploadedAt       time.Time `json:"uploaded_at"`
}

// StoredDocumentResponse es la respuesta de Persistencia (GET by-checksum y POST).
type StoredDocumentResponse struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	PDFHash   string `json:"pdf_hash"`
	FileName  string `json:"filename"`
	PageCount int    `json:"page_count"`
}
