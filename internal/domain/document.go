package domain

import "io"

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
