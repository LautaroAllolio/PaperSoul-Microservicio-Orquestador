package main

import (
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
)

const multipartBoundary = "papersoul-load-test-boundary"

var pdfFiles = []string{
	"2020-Scrum-Guide-Spanish-Latin-South-American.pdf",
	"Essential-Kanban-Condensed-Spanish.pdf",
	"Filosofia Lean.pdf",
	"scrum_manager_historias_usuario.pdf",
}

func main() {
	if err := prepareVegetaPayloads(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func prepareVegetaPayloads() error {
	outputDirectory := filepath.Join("tests", "vegeta-generated")
	if err := os.MkdirAll(outputDirectory, 0o755); err != nil {
		return fmt.Errorf("creating Vegeta payload directory: %w", err)
	}

	for index, filename := range pdfFiles {
		payloadPath := filepath.Join(outputDirectory, fmt.Sprintf("document-%02d.multipart", index+1))
		if err := createPayload(filename, payloadPath); err != nil {
			return err
		}
	}
	return nil
}

func createPayload(filename, payloadPath string) error {
	pdf, err := os.Open(filepath.Join("tests", "stress", "pdfs", filename))
	if err != nil {
		return fmt.Errorf("opening PDF %q: %w", filename, err)
	}
	defer pdf.Close()

	payload, err := os.Create(payloadPath)
	if err != nil {
		return fmt.Errorf("creating multipart payload %q: %w", payloadPath, err)
	}
	if err := writeMultipartPDF(payload, pdf, filename, multipartBoundary); err != nil {
		_ = payload.Close()
		return fmt.Errorf("writing multipart payload %q: %w", payloadPath, err)
	}
	if err := payload.Close(); err != nil {
		return fmt.Errorf("closing multipart payload %q: %w", payloadPath, err)
	}
	return nil
}

func writeMultipartPDF(destination io.Writer, pdf io.Reader, filename, boundary string) error {
	writer := multipart.NewWriter(destination)
	if err := writer.SetBoundary(boundary); err != nil {
		return fmt.Errorf("setting multipart boundary: %w", err)
	}

	headers := make(textproto.MIMEHeader)
	headers.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{
		"name":     "file",
		"filename": filename,
	}))
	headers.Set("Content-Type", "application/pdf")
	part, err := writer.CreatePart(headers)
	if err != nil {
		return fmt.Errorf("creating multipart file part: %w", err)
	}
	if _, err := io.Copy(part, pdf); err != nil {
		return fmt.Errorf("copying PDF into multipart payload: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("closing multipart payload: %w", err)
	}
	return nil
}
