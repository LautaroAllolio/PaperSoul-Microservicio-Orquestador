package handler

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/papersoul/orchestrator/internal/domain"
	"github.com/papersoul/orchestrator/internal/platform/config"
	errorsvc "github.com/papersoul/orchestrator/internal/platform/errors"
	"github.com/papersoul/orchestrator/internal/platform/problem"
)

const magicPDF = "%PDF-"

type ProcessHandler struct {
	svc DocumentService
	cfg config.Config
}

func NewProcessHandler(svc DocumentService, cfg config.Config) *ProcessHandler {
	return &ProcessHandler{svc: svc, cfg: cfg}
}

func (h *ProcessHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, h.cfg.MaxBodyBytes)

	fileName, data, size, err := h.readFilePart(r)
	if err != nil {
		h.writeProblem(w, r, err)
		return
	}

	res, err := h.svc.Process(r.Context(), &domain.ProcessInput{FileName: fileName, Size: size, File: data})
	if err != nil {
		h.writeProblem(w, r, err)
		return
	}

	writeOK(w, res)
}

// readFilePart decodifica el primer part con nombre "file" a un buffer acotado.
func (h *ProcessHandler) readFilePart(r *http.Request) (name string, data *bytes.Reader, size int64, err error) {
	mr, err := r.MultipartReader() // stream, NO vuelca a disco
	if err != nil {
		return "", nil, 0, errorsvc.ErrInvalidMultipart
	}
	var buf bytes.Buffer
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return "", nil, 0, errorsvc.ErrMissingFilePart
		}
		if err != nil {
			if isMaxBytesError(err) {
				return "", nil, 0, errorsvc.ErrFileTooLarge
			}
			return "", nil, 0, errorsvc.ErrInvalidMultipart
		}
		if part.FormName() != "file" {
			_ = part.Close() // ignorar otros campos / partes
			continue
		}
		// Sniff del magic bytes en el primer chunk: aborta sin bufferizar no-PDFs.
		br := bufio.NewReaderSize(part, 8)
		hdr, err := br.Peek(len(magicPDF))
		if err != nil || !bytes.Equal(hdr, []byte(magicPDF)) {
			return "", nil, 0, errorsvc.ErrInvalidPDFHeader
		}
		// Cap por-archivo: MaxFileSize+1 para detectar el exceso.
		n, err := buf.ReadFrom(io.LimitReader(br, h.cfg.MaxFileSize+1))
		if err != nil {
			if isMaxBytesError(err) {
				return "", nil, 0, errorsvc.ErrFileTooLarge
			}
			return "", nil, 0, errorsvc.ErrInvalidMultipart
		}
		if n > h.cfg.MaxFileSize {
			return "", nil, 0, errorsvc.ErrFileTooLarge
		}
		return part.FileName(), bytes.NewReader(buf.Bytes()), n, nil
	}
}

func isMaxBytesError(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}

func (h *ProcessHandler) writeProblem(w http.ResponseWriter, r *http.Request, err error) {
	problem.Write(w, r, errorsvc.Map(r, err))
}

type processMetadata struct {
	FileName  string `json:"fileName"`
	SizeBytes int64  `json:"sizeBytes"`
	PageCount int    `json:"pageCount"`
	Encrypted bool   `json:"encrypted"`
}

type processResponse struct {
	DocumentID string          `json:"documentId"`
	Status     string          `json:"status"`
	Checksum   string          `json:"checksum"`
	Metadata   processMetadata `json:"metadata"`
}

func writeOK(w http.ResponseWriter, res *domain.ProcessResult) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(processResponse{
		DocumentID: res.DocumentID,
		Status:     string(res.Status),
		Checksum:   res.Checksum,
		Metadata: processMetadata{
			FileName:  res.FileName,
			SizeBytes: res.Size,
			PageCount: res.PageCount,
			Encrypted: res.Encrypted,
		},
	})
}
