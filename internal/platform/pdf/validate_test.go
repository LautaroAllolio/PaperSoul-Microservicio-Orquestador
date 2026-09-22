package pdf_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"

	errorsvc "github.com/papersoul/orchestrator/internal/platform/errors"
	"github.com/papersoul/orchestrator/internal/platform/pdf"
	pdfcpuapi "github.com/pdfcpu/pdfcpu/pkg/api"
	pdfcpumodel "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func TestValidateValidPDFReturnsPageCountAndRewinds(t *testing.T) {
	for name, relaxed := range map[string]bool{"relaxed": true, "strict": false} {
		t.Run(name, func(t *testing.T) {
			r := bytes.NewReader(validFixture(t))

			pageCount, err := pdf.New(relaxed).Validate(r)
			if err != nil {
				t.Fatalf("PDF válido rechazado: %v", err)
			}
			if pageCount < 1 {
				t.Fatalf("pageCount = %d, quiero >= 1", pageCount)
			}
			if pos, _ := r.Seek(0, io.SeekCurrent); pos != 0 {
				t.Fatalf("reader no reposicionado al inicio: pos=%d", pos)
			}
		})
	}
}

func TestValidateCorruptPDFReturnsErrPDFCorrupted(t *testing.T) {
	valid := validFixture(t)
	corrupt := valid[:len(valid)/2]

	_, err := pdf.New(true).Validate(bytes.NewReader(corrupt))
	if !errors.Is(err, errorsvc.ErrPDFCorrupted) {
		t.Fatalf("err = %v (%T), quiero ErrPDFCorrupted", err, err)
	}
}

func TestValidateEncryptedPDFReturnsErrPDFEncrypted(t *testing.T) {
	encrypted := encryptedFixture(t)

	_, err := pdf.New(true).Validate(bytes.NewReader(encrypted))
	if !errors.Is(err, errorsvc.ErrPDFEncrypted) {
		t.Fatalf("err = %v (%T), quiero ErrPDFEncrypted", err, err)
	}
}

func validFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/valid.pdf")
	if err != nil {
		t.Fatalf("leyendo testdata/valid.pdf: %v", err)
	}
	return b
}

func encryptedFixture(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	conf := pdfcpumodel.NewAESConfiguration("user", "owner", 128)
	if err := pdfcpuapi.Encrypt(bytes.NewReader(validFixture(t)), &buf, conf); err != nil {
		t.Fatalf("generando PDF cifrado: %v", err)
	}
	return buf.Bytes()
}
