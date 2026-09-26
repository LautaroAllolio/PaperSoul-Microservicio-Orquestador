package domain_test

import (
	"errors"
	"testing"

	"github.com/papersoul/orchestrator/internal/domain"
)

func TestExtractResponseValidate(t *testing.T) {
	cases := []struct {
		nombre  string
		res     domain.ExtractResponse
		wantErr error
	}{
		{
			nombre: "pymupdf con páginas",
			res:    domain.ExtractResponse{ExtractionMethod: domain.ExtractionMethodPyMuPDF, PageCount: 1},
		},
		{
			nombre: "ocr con texto vacío",
			res:    domain.ExtractResponse{ExtractionMethod: domain.ExtractionMethodOCR, PageCount: 42},
		},
		{
			nombre:  "extraction_method desconocido",
			res:     domain.ExtractResponse{ExtractionMethod: "magia", PageCount: 1},
			wantErr: domain.ErrInvalidExtractionMethod,
		},
		{
			nombre:  "extraction_method ausente",
			res:     domain.ExtractResponse{PageCount: 1},
			wantErr: domain.ErrInvalidExtractionMethod,
		},
		{
			nombre:  "page_count 0",
			res:     domain.ExtractResponse{ExtractionMethod: domain.ExtractionMethodPyMuPDF},
			wantErr: domain.ErrInvalidPageCount,
		},
		{
			nombre:  "page_count negativo",
			res:     domain.ExtractResponse{ExtractionMethod: domain.ExtractionMethodOCR, PageCount: -3},
			wantErr: domain.ErrInvalidPageCount,
		},
	}

	for _, tc := range cases {
		t.Run(tc.nombre, func(t *testing.T) {
			err := tc.res.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, quiero nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() = %v, quiero %v", err, tc.wantErr)
			}
		})
	}
}
