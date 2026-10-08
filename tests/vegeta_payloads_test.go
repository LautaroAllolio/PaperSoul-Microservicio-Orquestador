package main

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"testing"
)

func TestWriteMultipartPDFUsesOrchestratorFileContract(t *testing.T) {
	const (
		boundary = "papersoul-load-test-boundary"
		filename = "Filosofia Lean.pdf"
		content  = "%PDF-1.7\nsample content"
	)

	var payload bytes.Buffer
	if err := writeMultipartPDF(&payload, strings.NewReader(content), filename, boundary); err != nil {
		t.Fatalf("writeMultipartPDF() error = %v", err)
	}

	reader := multipart.NewReader(&payload, boundary)
	part, err := reader.NextPart()
	if err != nil {
		t.Fatalf("NextPart() error = %v", err)
	}

	if part.FormName() != "file" {
		t.Errorf("part name = %q, want %q", part.FormName(), "file")
	}
	if part.FileName() != filename {
		t.Errorf("filename = %q, want %q", part.FileName(), filename)
	}
	if got := part.Header.Get("Content-Type"); got != "application/pdf" {
		t.Errorf("part Content-Type = %q, want application/pdf", got)
	}

	gotContent, err := io.ReadAll(part)
	if err != nil {
		t.Fatalf("reading PDF part: %v", err)
	}
	if string(gotContent) != content {
		t.Errorf("PDF content = %q, want %q", gotContent, content)
	}

	mediaType, parameters, err := mime.ParseMediaType("multipart/form-data; boundary=" + boundary)
	if err != nil {
		t.Fatalf("ParseMediaType() error = %v", err)
	}
	if mediaType != "multipart/form-data" || parameters["boundary"] != boundary {
		t.Errorf("multipart content type = %q, parameters = %v", mediaType, parameters)
	}
	disposition, parameters, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	if err != nil {
		t.Fatalf("ParseMediaType(Content-Disposition) error = %v", err)
	}
	if disposition != "form-data" || parameters["name"] != "file" || parameters["filename"] != filename {
		t.Errorf("Content-Disposition = %q; parameters = %v", disposition, parameters)
	}
}
