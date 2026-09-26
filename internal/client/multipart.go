package client

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
)

const (
	// fieldFile es el nombre del campo multipart que transporta el binario.
	fieldFile = "file"
	// fieldChecksum es el campo multipart con el SHA-256 del binario.
	fieldChecksum = "checksum"
)

// NewMultipartPipe arma el body multipart de la request hacia el Extractor sobre
// un io.Pipe y devuelve la longitud EXACTA del body, para poder fijar
// ContentLength y evitar transfer-chunked sin duplicar el binario en memoria.
//
// El sobre (los headers de las partes más el closing boundary) se mide primero
// con un multipart.Writer sobre un buffer de unos cientos de bytes; después se
// retransmite byte a byte con el mismo boundary. Así el sobre lo genera
// mime/multipart —con el escaping correcto del filename— y el binario solo
// existe una vez en memoria: el io.Pipe aplica backpressure contra el socket.
//
// size es el número de bytes del binario (lo calcula el handler) y debe ser > 0.
func NewMultipartPipe(file io.Reader, fileName, checksum string, size int64) (body io.Reader, contentType string, totalLen int64, err error) {
	if file == nil {
		return nil, "", 0, errors.New("client: NewMultipartPipe sin file")
	}
	if size <= 0 {
		return nil, "", 0, fmt.Errorf("client: NewMultipartPipe con size %d, quiero > 0", size)
	}

	// 1. Medir el sobre con el mismo boundary que va a viajar en la request.
	var envelope bytes.Buffer
	mw := multipart.NewWriter(&envelope)
	if err = mw.WriteField(fieldChecksum, checksum); err != nil {
		return nil, "", 0, fmt.Errorf("client: escribiendo el campo %q: %w", fieldChecksum, err)
	}
	if _, err = mw.CreateFormFile(fieldFile, fileName); err != nil {
		return nil, "", 0, fmt.Errorf("client: creando la parte %q: %w", fieldFile, err)
	}
	closing := fmt.Sprintf("\r\n--%s--\r\n", mw.Boundary()) // lo que escribe Close()
	if err = mw.Close(); err != nil {
		return nil, "", 0, fmt.Errorf("client: cerrando el multipart: %w", err)
	}

	measured := envelope.Bytes()
	if len(measured) < len(closing) || !bytes.Equal(measured[len(measured)-len(closing):], []byte(closing)) {
		return nil, "", 0, errors.New("client: el sobre multipart medido no termina con el closing boundary esperado")
	}
	prefix := measured[:len(measured)-len(closing)]
	suffix := []byte(closing)
	totalLen = int64(len(prefix)+len(suffix)) + size

	// 2. Transmitir: prefijo + binario + sufijo, sin materializar el binario.
	pr, pw := io.Pipe()
	go func() {
		if _, err := pw.Write(prefix); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if _, err := io.CopyN(pw, file, size); err != nil {
			// io.Pipe interpreta CloseWithError(io.EOF) como un cierre limpio:
			// reempaquetamos el EOF para que el transporte HTTP aborte la request
			// en vez de mandar un body más corto que el ContentLength declarado.
			if errors.Is(err, io.EOF) {
				err = fmt.Errorf("client: el binario tiene menos bytes que el size declarado (%d)", size)
			}
			_ = pw.CloseWithError(err)
			return
		}
		if _, err := pw.Write(suffix); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()

	return pr, mw.FormDataContentType(), totalLen, nil
}
