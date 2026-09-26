package client_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/papersoul/orchestrator/internal/client"
)

func TestNewMultipartPipeArmaElSobreYElBinarioEnOrden(t *testing.T) {
	content := pdfBytes(4096)

	body, contentType, totalLen, err := client.NewMultipartPipe(
		bytes.NewReader(content), "factura.pdf", testChecksum, int64(len(content)),
	)
	if err != nil {
		t.Fatalf("NewMultipartPipe = %v, quiero nil", err)
	}
	if !strings.HasPrefix(contentType, "multipart/form-data; boundary=") {
		t.Fatalf("contentType = %q, quiero multipart/form-data con boundary", contentType)
	}

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("leyendo el body del pipe: %v", err)
	}
	if int64(len(raw)) != totalLen {
		t.Fatalf("totalLen = %d pero el body mide %d: el ContentLength precalculado no es exacto", totalLen, len(raw))
	}
	if totalLen <= int64(len(content)) {
		t.Fatalf("totalLen = %d, quiero que incluya el sobre multipart además del binario (%d bytes)", totalLen, len(content))
	}
	if !bytes.Contains(raw, content) {
		t.Fatalf("el body del pipe no contiene el binario (%d bytes)", len(raw))
	}

	checksumField, fileName, fileContent := parseMultipart(t, contentType, raw)
	if fileName != "factura.pdf" {
		t.Fatalf("filename = %q, quiero factura.pdf", fileName)
	}
	if !bytes.Equal(fileContent, content) {
		t.Fatalf("el binario en el pipe difiere del original (%d vs %d bytes)", len(fileContent), len(content))
	}
	if checksumField != testChecksum {
		t.Fatalf("campo checksum = %q, quiero %q", checksumField, testChecksum)
	}
}

func TestNewMultipartPipeConSizeInvalidoDevuelveError(t *testing.T) {
	for _, size := range []int64{0, -1} {
		if _, _, _, err := client.NewMultipartPipe(bytes.NewReader(pdfBytes(8)), "doc.pdf", testChecksum, size); err == nil {
			t.Fatalf("NewMultipartPipe con size=%d = nil, quiero error", size)
		}
	}
}

func TestNewMultipartPipeConFileNilDevuelveError(t *testing.T) {
	if _, _, _, err := client.NewMultipartPipe(nil, "doc.pdf", testChecksum, 10); err == nil {
		t.Fatal("NewMultipartPipe con file nil = nil, quiero error")
	}
}

func TestNewMultipartPipePropagaElErrorSiElBinarioEsMasCorto(t *testing.T) {
	// El size declarado no coincide con el binario disponible: el error tiene que
	// viajar por el pipe para que http.Client aborte la request, no quedar colgado.
	body, _, _, err := client.NewMultipartPipe(bytes.NewReader(pdfBytes(10)), "doc.pdf", testChecksum, 1000)
	if err != nil {
		t.Fatalf("NewMultipartPipe = %v, quiero nil (el error recién aparece al leer)", err)
	}

	if _, err := io.ReadAll(body); err == nil {
		t.Fatal("io.ReadAll = nil, quiero error: el binario es más corto que el size declarado")
	}
}
