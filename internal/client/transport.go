package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	errorsvc "github.com/papersoul/orchestrator/internal/platform/errors"
	"github.com/papersoul/orchestrator/internal/platform/reqid"
)

// maxDownstreamErrorBody acota lo que se lee del body de error de un downstream:
// alcanza para diagnóstico y nunca se re-expone al cliente final.
const maxDownstreamErrorBody = 4 << 10

// Options son los parámetros de construcción de un client HTTP downstream.
type Options struct {
	// BaseURL es la raíz del microservicio, sin path (ej. "http://extractor:8000").
	BaseURL string
	// Timeout es el deadline de la llamada completa (conexión, escritura del body
	// y lectura de la respuesta). Si es 0, el deadline es el del contexto.
	Timeout time.Duration
}

// newHTTPClient arma el http.Client compartido por los clients.
func newHTTPClient(timeout time.Duration) *http.Client {
	c := &http.Client{
		// No seguimos redirects: el binario nunca debe viajar a otro host.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if timeout > 0 {
		c.Timeout = timeout
	}
	return c
}

// setCommonHeaders propaga la correlación y declara qué se espera de vuelta.
func setCommonHeaders(req *http.Request, ctx context.Context) {
	req.Header.Set("Accept", "application/json")
	if id := reqid.From(ctx); id != "" {
		req.Header.Set("X-Correlation-Id", id)
	}
}

// isTimeout distingue un deadline agotado de un error de red común: el error que
// devuelve http.Client.Timeout no siempre envuelve context.DeadlineExceeded.
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// transportError mapea un fallo de transporte (no hubo respuesta) al sentinel del
// downstream. Envuelve el sentinel con %w para que errors.Is siga funcionando.
func transportError(timeoutSentinel, unavailableSentinel error, err error) error {
	if isTimeout(err) {
		return fmt.Errorf("%w: %v", timeoutSentinel, err)
	}
	return fmt.Errorf("%w: %v", unavailableSentinel, err)
}

// downstreamStatusError arma el error de una respuesta no-2xx. El body del
// tercero se lee acotado y queda solo en el mensaje para diagnóstico interno:
// nunca se re-expone al cliente del orquestador.
func downstreamStatusError(sentinel error, resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxDownstreamErrorBody))
	drainAndClose(resp)

	detail := bytes.TrimSpace(raw)
	if len(detail) > 0 {
		return fmt.Errorf("%w: status %d: %s", sentinel, resp.StatusCode, detail)
	}
	return fmt.Errorf("%w: status %d", sentinel, resp.StatusCode)
}

// decodeJSON decodifica el body de un 2xx en dst. Body vacío, JSON roto o
// cualquier otra falla se reporta con invalidSentinel.
func decodeJSON(resp *http.Response, dst any, invalidSentinel error) error {
	err := json.NewDecoder(resp.Body).Decode(dst)
	drainAndClose(resp)
	if err != nil {
		return fmt.Errorf("%w: %v", invalidSentinel, err)
	}
	return nil
}

// drainAndClose descarta el body y cierra la response para que la conexión
// vuelva al pool en lugar de quedar Thunkeada.
func drainAndClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDownstreamErrorBody))
	_ = resp.Body.Close()
}

// is2xx es el criterio de éxito de todos los clients (no solo 200: Persistence
// responde 201 al Store).
func is2xx(code int) bool {
	return code >= http.StatusOK && code <= 299
}

// baseURL normaliza la raíz del downstream: sin slash final para no duplicar la
// barra al concatenar el path.
func baseURL(raw string) string {
	return strings.TrimRight(raw, "/")
}

// internalError envuelve ErrInternal con el detalle de la causa.
func internalError(err error) error {
	return fmt.Errorf("%w: %v", errorsvc.ErrInternal, err)
}
