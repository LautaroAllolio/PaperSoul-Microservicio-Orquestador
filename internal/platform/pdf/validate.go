package pdf

import (
	"errors"
	"io"

	errorsvc "github.com/papersoul/orchestrator/internal/platform/errors"
	pdfcpuapi "github.com/pdfcpu/pdfcpu/pkg/api"
	pdfcpucore "github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	pdfcpumodel "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// pdfcpu crea por defecto ~/.config/pdfcpu (config.yml, fuentes y certificados).
// El contenedor corre con rootfs read-only y como usuario nonroot, así que ese
// MkdirAll falla y NewDefaultConfiguration entra en panic. Deshabilitar el dir
// de configuración usa los defaults en memoria: el Orquestador no escribe a
// disco y la validación sigue siendo idéntica.
func init() {
	pdfcpuapi.DisableConfigDir()
}

type Validator struct {
	relaxed bool
}

func New(relaxed bool) *Validator {
	return &Validator{relaxed: relaxed}
}

func (v *Validator) Validate(r io.ReadSeeker) (int, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, errorsvc.ErrInternal
	}

	conf := pdfcpumodel.NewDefaultConfiguration()
	if v.relaxed {
		conf.ValidationMode = pdfcpumodel.ValidationRelaxed
	} else {
		conf.ValidationMode = pdfcpumodel.ValidationStrict
	}
	conf.Cmd = pdfcpumodel.VALIDATE

	ctx, err := pdfcpuapi.ReadAndValidate(r, conf)
	if err != nil {
		if isEncryptionError(err) {
			return 0, errorsvc.ErrPDFEncrypted
		}
		return 0, errorsvc.ErrPDFCorrupted
	}
	if ctx.Encrypt != nil {
		return 0, errorsvc.ErrPDFEncrypted
	}

	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, errorsvc.ErrInternal
	}
	return ctx.PageCount, nil
}

func isEncryptionError(err error) bool {
	return errors.Is(err, pdfcpucore.ErrWrongPassword) ||
		errors.Is(err, pdfcpucore.ErrEncrypted)
}
