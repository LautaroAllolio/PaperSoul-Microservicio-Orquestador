// Test del contrato OpenAPI 3.1. Vive en el mismo directorio que el yaml y no
// lleva build tag: el contrato es parte del build, así que `make test` tiene que
// detectarlo si alguien edita api/openapi.yaml a mano.
//
// Hasta la Task 8 nadie parseaba el archivo (fue un bug real: un escalar sin
// quoting lo dejaba como YAML inválido y nadie lo notó). Estos tests son el
// "lint" del contrato: no reemplazan a un validador OpenAPI completo, pero
// fijan los invariantes que el código y el plan dan por ciertos, entre ellos
// que todo status de la matriz de errores 2.6 esté declarado y responda
// application/problem+json con el schema Problem.
package api_test

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const (
	rutaContrato = "openapi.yaml"
	pathProceso  = "/api/v1/documents/process"
	refProblem   = "#/components/schemas/Problem"
	mediaProblem = "application/problem+json"
)

// statusDeError es la matriz 2.6 del plan: cada status de error que el borde
// puede devolver, con el nombre del component response que lo modela. El
// comentario del 404/405 importa: el router los produce (chi NotFound y
// MethodNotAllowed, este último con el header Allow) y el contrato los tenía
// solo mencionados en la descripción del response `default`.
var statusDeError = map[int]string{
	400: "BadRequest",
	404: "NotFound",
	405: "MethodNotAllowed",
	413: "PayloadTooLarge",
	422: "UnprocessableEntity",
	500: "InternalServerError",
	502: "BadGateway",
	503: "ServiceUnavailable",
	504: "GatewayTimeout",
}

// cargarContrato parsea el yaml. Un error de parseo es un fallo del test, no un
// skip: fue exactamente el modo de falla que nadie detectó durante siete tasks.
func cargarContrato(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(rutaContrato)
	if err != nil {
		t.Fatalf("leyendo %s: %v", rutaContrato, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s no es YAML válido: %v", rutaContrato, err)
	}
	if doc == nil {
		t.Fatalf("%s parsea a nil", rutaContrato)
	}
	return doc
}

// respuestasDelProceso devuelve el mapa responses de la operación POST del
// endpoint de procesado.
func respuestasDelProceso(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	post := hijoMapa(t, hijoMapa(t, hijoMapa(t, doc, "paths"), pathProceso), "post")
	return hijoMapa(t, post, "responses")
}

// resolver sigue un $ref local (#/components/responses/X, #/components/schemas/X)
// y devuelve el nodo apuntado.
func resolver(t *testing.T, doc map[string]any, nodo map[string]any) map[string]any {
	t.Helper()
	ref, ok := nodo["$ref"].(string)
	if !ok {
		return nodo
	}
	if !strings.HasPrefix(ref, "#/") {
		t.Fatalf("solo se resuelven $ref locales, encontré %q", ref)
	}
	var actual any = doc
	for _, parte := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		siguiente, ok := actual.(map[string]any)
		if !ok {
			t.Fatalf("no se puede seguir %q: %q no es un mapa", ref, parte)
		}
		if actual, ok = siguiente[parte]; !ok {
			t.Fatalf("no se puede seguir %q: falta %q", ref, parte)
		}
	}
	resuelto, ok := actual.(map[string]any)
	if !ok {
		t.Fatalf("el $ref %q no apunta a un mapa", ref)
	}
	return resuelto
}

func TestElContratoDeclaraLaVersionOpenAPI31(t *testing.T) {
	doc := cargarContrato(t)

	if v := hijoTexto(t, doc, "openapi"); v != "3.1.0" {
		t.Errorf("openapi = %q, quiero %q", v, "3.1.0")
	}
}

func TestElContratoExponeElEndpointDeProcesado(t *testing.T) {
	doc := cargarContrato(t)

	paths := hijoMapa(t, doc, "paths")
	if _, ok := paths[pathProceso]; !ok {
		t.Fatalf("falta el path %s en el contrato (declarados: %v)", pathProceso, claves(paths))
	}
	if len(paths) != 1 {
		t.Errorf("el orquestador expone un solo endpoint, el contrato declara %d: %v", len(paths), claves(paths))
	}
	if op := hijoTexto(t, hijoMapa(t, hijoMapa(t, paths, pathProceso), "post"), "operationId"); op != "processDocument" {
		t.Errorf("operationId = %q, quiero %q", op, "processDocument")
	}
}

func TestElContratoExigeElCampoFileComoBinarioPDF(t *testing.T) {
	doc := cargarContrato(t)
	post := hijoMapa(t, hijoMapa(t, hijoMapa(t, doc, "paths"), pathProceso), "post")

	body := hijoMapa(t, post, "requestBody")
	if req, ok := body["required"].(bool); !ok || !req {
		t.Errorf("requestBody.required = %v, quiero true", body["required"])
	}
	media := hijoMapa(t, hijoMapa(t, body, "content"), "multipart/form-data")
	schema := hijoMapa(t, media, "schema")

	if falta := hijoSlice(t, schema, "required"); !contiene(falta, "file") {
		t.Errorf("el campo file debería ser obligatorio, required = %v", falta)
	}
	campo := hijoMapa(t, hijoMapa(t, schema, "properties"), "file")
	if f := hijoTexto(t, campo, "format"); f != "binary" {
		t.Errorf("file.format = %q, quiero %q", f, "binary")
	}
	// El encoding fija el media type de la parte: el extractor downstream decide
	// por esto si acepta el binario, así que no es cosmético.
	encoding := hijoTexto(t, hijoMapa(t, hijoMapa(t, media, "encoding"), "file"), "contentType")
	if encoding != "application/pdf" {
		t.Errorf("encoding.file.contentType = %q, quiero application/pdf", encoding)
	}
}

func TestElContratoDeclaraTodosLosStatusDeLaMatrizDeErrores(t *testing.T) {
	doc := cargarContrato(t)
	responses := respuestasDelProceso(t, doc)

	for status, nombre := range statusDeError {
		clave := strconv.Itoa(status)
		if _, ok := responses[clave]; !ok {
			t.Errorf("el contrato no declara el response %s (%s)", clave, nombre)
		}
	}
}

func TestElContratoNoDeclaraStatusDeErrorInesperados(t *testing.T) {
	doc := cargarContrato(t)
	responses := respuestasDelProceso(t, doc)

	for clave := range responses {
		if clave == "default" || clave == "200" {
			continue
		}
		status, err := strconv.Atoi(clave)
		if err != nil {
			t.Errorf("clave de response no numérica y distinta de \"default\": %q", clave)
			continue
		}
		if _, ok := statusDeError[status]; !ok {
			t.Errorf("el contrato declara el response %d, que no está en la matriz de errores del plan", status)
		}
	}
}

func TestTodoStatusDeErrorRespondeProblemJSON(t *testing.T) {
	doc := cargarContrato(t)
	responses := respuestasDelProceso(t, doc)

	for clave, nodo := range responses {
		if clave == "200" {
			continue
		}
		res := resolver(t, doc, nodo.(map[string]any))
		content := hijoMapa(t, res, "content")
		if _, ok := content[mediaProblem]; !ok {
			t.Errorf("el response %s no declara %s (declara: %v)", clave, mediaProblem, claves(content))
			continue
		}
		ref := hijoTexto(t, hijoMapa(t, hijoMapa(t, content, mediaProblem), "schema"), "$ref")
		if ref != refProblem {
			t.Errorf("el response %s no referencia al schema Problem (ref = %q)", clave, ref)
		}
	}
}

func TestElResponse405DeclaraElHeaderAllow(t *testing.T) {
	doc := cargarContrato(t)
	responses := respuestasDelProceso(t, doc)

	nodo, ok := responses["405"]
	if !ok {
		t.Fatal("el contrato no declara el response 405")
	}
	// El handler de MethodNotAllowed setea Allow antes de escribir el problem;
	// el cliente necesita la lista de métodos para reintentar bien.
	if _, ok := hijoMapa(t, resolver(t, doc, nodo.(map[string]any)), "headers")["Allow"]; !ok {
		t.Error("el response 405 no declara el header Allow")
	}
}

func TestElContratoDeclaraElHeaderDeCorrelacionEnLaPeticionYEnLaRespuesta(t *testing.T) {
	doc := cargarContrato(t)
	post := hijoMapa(t, hijoMapa(t, hijoMapa(t, doc, "paths"), pathProceso), "post")

	params := hijoSlice(t, post, "parameters")
	if len(params) != 1 {
		t.Fatalf("esperaba 1 parámetro en la operación, hay %d", len(params))
	}
	ref := hijoTexto(t, params[0].(map[string]any), "$ref")
	param := resolver(t, doc, params[0].(map[string]any))
	if ref != "#/components/parameters/CorrelationId" {
		t.Errorf("el parámetro no es el CorrelationId compartido (ref = %q)", ref)
	}
	if nombre := hijoTexto(t, param, "name"); nombre != "X-Correlation-Id" {
		t.Errorf("name = %q, quiero X-Correlation-Id", nombre)
	}
	if in := hijoTexto(t, param, "in"); in != "header" {
		t.Errorf("in = %q, quiero header", in)
	}

	// El RequestID escribe el header en todas las respuestas, no solo en el 200;
	// el contrato lo documenta en el 200 (único cuerpo de éxito) y en el
	// parámetro de la petición.
	headers := hijoMapa(t, resolver(t, doc, hijoMapa(t, respuestasDelProceso(t, doc), "200")), "headers")
	if _, declarado := headers["X-Correlation-Id"]; !declarado {
		t.Errorf("el response 200 no declara el header X-Correlation-Id (declarados: %v)", claves(headers))
	}
	if _, declarado := hijoMapa(t, doc, "components")["headers"].(map[string]any)["CorrelationId"]; !declarado {
		t.Error("components.headers no declara el header compartido CorrelationId")
	}
}

func TestElSchemaProblemTieneLosCamposObligatoriosDeRFC9457(t *testing.T) {
	doc := cargarContrato(t)
	schema := hijoMapa(t, hijoMapa(t, hijoMapa(t, doc, "components"), "schemas"), "Problem")

	requeridos := hijoSlice(t, schema, "required")
	for _, campo := range []string{"type", "title", "status"} {
		if !contiene(requeridos, campo) {
			t.Errorf("Problem.required no incluye %q (required = %v)", campo, requeridos)
		}
	}
	if len(requeridos) != 3 {
		t.Errorf("Problem.required debería tener exactamente type, title y status: %v", requeridos)
	}

	// detail, instance e invalid_params son opcionales en RFC 9457: el
	// omitempty del modelo Go depende de eso.
	propiedades := hijoMapa(t, schema, "properties")
	for _, campo := range []string{"detail", "instance", "invalid_params"} {
		if _, ok := propiedades[campo]; !ok {
			t.Errorf("Problem.properties no declara %q", campo)
		}
		if contiene(requeridos, campo) {
			t.Errorf("%q no debería ser obligatorio en Problem", campo)
		}
	}
	invalid := hijoMapa(t, propiedades, "invalid_params")
	if ref := hijoTexto(t, hijoMapa(t, invalid, "items"), "$ref"); ref != "#/components/schemas/InvalidParam" {
		t.Errorf("invalid_params.items no referencia a InvalidParam (ref = %q)", ref)
	}
}

func TestElEnumDeStatusDel200SoloAdmiteProcessedYReused(t *testing.T) {
	doc := cargarContrato(t)
	schema := hijoMapa(t, hijoMapa(t, hijoMapa(t, doc, "components"), "schemas"), "ProcessDocumentResponse")

	enum := hijoSlice(t, hijoMapa(t, hijoMapa(t, schema, "properties"), "status"), "enum")
	if len(enum) != 2 || !contiene(enum, "PROCESSED") || !contiene(enum, "REUSED") {
		t.Errorf("el enum de status debería ser exactamente [PROCESSED, REUSED], es %v", enum)
	}
}

// TestElStatusDeCadaEjemploCoincideConElStatusDeSuRespuesta ata los ejemplos
// del contrato a los status reales: un ejemplo con status 400 pegado al 422 es
// la clase de contradicción que el cliente termina copiando a su documentación.
func TestElStatusDeCadaEjemploCoincideConElStatusDeSuRespuesta(t *testing.T) {
	doc := cargarContrato(t)
	responses := respuestasDelProceso(t, doc)

	for clave, nodo := range responses {
		status, err := strconv.Atoi(clave)
		if err != nil {
			continue // `default` no tiene un status con el que comparar
		}
		res := resolver(t, doc, nodo.(map[string]any))
		for media, cuerpo := range hijoMapa(t, res, "content") {
			mm := cuerpo.(map[string]any)
			if ej, ok := mm["example"]; ok {
				revisarEjemplo(t, clave, status, media, ej)
			}
			// `examples` es opcional: un media type puede traer solo `example`.
			variantes, ok := mm["examples"].(map[string]any)
			if !ok {
				continue
			}
			for nombre, variante := range variantes {
				revisarEjemplo(t, clave, status, media+"/"+nombre, hijoMapa(t, variante.(map[string]any), "value"))
			}
		}
	}
}

func revisarEjemplo(t *testing.T, clave string, status int, donde string, ejemplo any) {
	t.Helper()
	m, ok := ejemplo.(map[string]any)
	if !ok {
		return
	}
	v, ok := m["status"]
	if !ok {
		return
	}
	if got, ok := v.(int); !ok || got != status {
		t.Errorf("el ejemplo %s del response %s declara status %v, pero el response es %d", donde, clave, v, status)
	}
}

// --- helpers de navegación -------------------------------------------------
// El contrato se recorre con yaml.Node en un validador completo; acá alcanza con
// decodificar a map[string]any y fallar fuerte cuando algo no está donde se
// espera: cada aserción tiene que decir qué falta, no que descolocó el índice.

func hijo(t *testing.T, m map[string]any, clave string) any {
	t.Helper()
	v, ok := m[clave]
	if !ok {
		t.Fatalf("falta la clave %q (presentes: %v)", clave, claves(m))
	}
	return v
}

func hijoMapa(t *testing.T, m map[string]any, clave string) map[string]any {
	t.Helper()
	v, ok := hijo(t, m, clave).(map[string]any)
	if !ok {
		t.Fatalf("la clave %q no es un mapa", clave)
	}
	return v
}

func hijoSlice(t *testing.T, m map[string]any, clave string) []any {
	t.Helper()
	v, ok := hijo(t, m, clave).([]any)
	if !ok {
		t.Fatalf("la clave %q no es una lista", clave)
	}
	return v
}

func hijoTexto(t *testing.T, m map[string]any, clave string) string {
	t.Helper()
	v, ok := hijo(t, m, clave).(string)
	if !ok {
		t.Fatalf("la clave %q no es un string", clave)
	}
	return v
}

func claves(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func contiene(lista []any, valor string) bool {
	for _, v := range lista {
		if s, ok := v.(string); ok && s == valor {
			return true
		}
	}
	return false
}
