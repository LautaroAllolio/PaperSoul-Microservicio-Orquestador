package problem

import (
	"encoding/json"
	"net/http"
)

const (
	TypeInvalidMultipart       = "urn:papersoul:orchestrator:invalid-multipart"
	TypeFileTooLarge           = "urn:papersoul:orchestrator:file-too-large"
	TypeInvalidPDFHeader       = "urn:papersoul:orchestrator:invalid-pdf-header"
	TypeInvalidPDF             = "urn:papersoul:orchestrator:invalid-pdf"
	TypeEncryptedPDF           = "urn:papersoul:orchestrator:encrypted-pdf"
	TypeExtractorUnavailable   = "urn:papersoul:orchestrator:extractor-unavailable"
	TypePersistenceUnavailable = "urn:papersoul:orchestrator:persistence-unavailable"
	TypeDownstreamTimeout      = "urn:papersoul:orchestrator:downstream-timeout"
	TypeInternalError          = "urn:papersoul:orchestrator:internal-error"
)

type Problem struct {
	Type          string         `json:"type"`
	Title         string         `json:"title"`
	Status        int            `json:"status"`
	Detail        string         `json:"detail,omitempty"`
	Instance      string         `json:"instance,omitempty"`
	InvalidParams []InvalidParam `json:"invalid_params,omitempty"`
}

type InvalidParam struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

func Write(w http.ResponseWriter, r *http.Request, p Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}
