package errors

import "errors"

var (
	ErrInvalidMultipart         = errors.New("invalid multipart body")
	ErrMissingFilePart          = errors.New("missing file part")
	ErrFileTooLarge             = errors.New("file exceeds size limit")
	ErrInvalidPDFHeader         = errors.New("invalid pdf magic bytes")
	ErrPDFCorrupted             = errors.New("pdf is corrupted")
	ErrPDFEncrypted             = errors.New("pdf is encrypted")
	ErrExtractorUnavailable     = errors.New("extractor unavailable")
	ErrExtractorTimeout         = errors.New("extractor timed out")
	ErrExtractorInvalidResponse = errors.New("extractor returned invalid response")
	ErrPersistenceUnavailable   = errors.New("persistence unavailable")
	ErrPersistenceTimeout       = errors.New("persistence timed out")
	ErrDocumentNotFound         = errors.New("document not found")
	ErrPersistenceConflict      = errors.New("persistence checksum conflict")
	ErrInternal                 = errors.New("internal error")
)
