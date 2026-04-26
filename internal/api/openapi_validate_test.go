//go:build !nocontainers

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	legacyrouter "github.com/getkin/kin-openapi/routers/legacy"
)

var loadSpecOnce = sync.OnceValues(func() (*openapi3.T, error) {
	_, thisFile, _, _ := runtime.Caller(0)
	specPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "api", "openapi.yaml")
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(specPath)
	if err != nil {
		return nil, err
	}
	if err := doc.Validate(context.Background()); err != nil {
		return nil, err
	}
	return doc, nil
})

func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := loadSpecOnce()
	if err != nil {
		t.Fatalf("load OpenAPI spec: %v", err)
	}
	return doc
}

// checkResponse validates rec against the OpenAPI spec for the given request.
// Paths not present in the spec (e.g. webhook routes) are silently skipped.
func checkResponse(t *testing.T, req *http.Request, rec *httptest.ResponseRecorder) {
	t.Helper()
	doc := loadSpec(t)

	router, err := legacyrouter.NewRouter(doc)
	if err != nil {
		t.Fatalf("create OpenAPI router: %v", err)
	}

	// kin-openapi requires an absolute URL to match server entries in the spec.
	reqCopy := req.Clone(req.Context())
	reqCopy.RequestURI = ""
	reqCopy.URL.Scheme = "http"
	reqCopy.URL.Host = "localhost:8080"

	route, pathParams, err := router.FindRoute(reqCopy)
	if err != nil {
		// Path not in spec – skip validation (e.g. webhook endpoints).
		t.Logf("skipping OpenAPI validation for %s %s (not in spec)", req.Method, req.URL.Path)
		return
	}

	reqInput := &openapi3filter.RequestValidationInput{
		Request:    reqCopy,
		PathParams: pathParams,
		Route:      route,
		Options: &openapi3filter.Options{
			AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
			ExcludeRequestBody: true,
		},
	}

	body := rec.Body.Bytes()
	respInput := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: reqInput,
		Status:                 rec.Code,
		Header:                 rec.Result().Header,
		Body:                   io.NopCloser(bytes.NewReader(body)),
		Options: &openapi3filter.Options{
			AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
		},
	}

	if err := openapi3filter.ValidateResponse(context.Background(), respInput); err != nil {
		t.Errorf("response to %s %s (status %d) violates OpenAPI spec: %v",
			req.Method, req.URL.Path, rec.Code, err)
	}
}

func requireSpecRoute(t *testing.T, req *http.Request) {
	t.Helper()
	doc := loadSpec(t)
	router, err := legacyrouter.NewRouter(doc)
	if err != nil {
		t.Fatalf("create OpenAPI router: %v", err)
	}

	reqCopy := req.Clone(req.Context())
	reqCopy.RequestURI = ""
	reqCopy.URL.Scheme = "http"
	reqCopy.URL.Host = "localhost:8080"

	if _, _, err := router.FindRoute(reqCopy); err != nil {
		t.Fatalf("OpenAPI spec missing route for %s %s: %v", req.Method, req.URL.Path, err)
	}
}

// extractID decodes the task ID from a create-task response body.
func extractID(t *testing.T, body []byte) string {
	t.Helper()
	var env struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode task response: %v", err)
	}
	return env.Data.ID
}

// createTestTask creates a minimal task via the API and returns its ID.
func createTestTask(t *testing.T, srv http.Handler) string {
	t.Helper()
	body := `{"prompt":"Fix the bug in https://github.com/test/repo"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create task: status = %d, body: %s", rec.Code, rec.Body)
	}
	return extractID(t, rec.Body.Bytes())
}

// ---- Spec self-validation ----

func TestOpenAPISpecValid(t *testing.T) {
	loadSpec(t) // fatals on invalid spec
}

// ---- GET /health ----

func TestOpenAPI_HealthRoot(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	checkResponse(t, req, rec)
}

// ---- GET /api/v1/health ----

func TestOpenAPI_HealthAPI(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	checkResponse(t, req, rec)
}

// ---- POST /api/v1/tasks ----

func TestOpenAPI_CreateTask_201(t *testing.T) {
	srv := testServer(t)
	body := `{"prompt":"Fix the bug in https://github.com/test/repo"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", rec.Code, rec.Body)
	}
	checkResponse(t, req, rec)
}

func TestOpenAPI_CreateTask_400_MissingFields(t *testing.T) {
	srv := testServer(t)
	body := `{"prompt":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	checkResponse(t, req, rec)
}

func TestOpenAPI_CreateTask_400_InvalidHarness(t *testing.T) {
	srv := testServer(t)
	body := `{"prompt":"Fix it","harness":"invalid"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	checkResponse(t, req, rec)
}

func TestOpenAPI_CreateTask_ReviewMode_201(t *testing.T) {
	srv := testServer(t)
	body := `{"prompt":"Review https://github.com/test/repo/pull/7 for security issues"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", rec.Code, rec.Body)
	}
	checkResponse(t, req, rec)
}

// ---- GET /api/v1/tasks ----

func TestOpenAPI_ListTasks_200_Empty(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	checkResponse(t, req, rec)
}

func TestOpenAPI_ListTasks_200_WithFilter(t *testing.T) {
	srv := testServer(t)
	createTestTask(t, srv)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks?status=pending&limit=10&offset=0", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	checkResponse(t, req, rec)
}

// ---- GET /api/v1/tasks/{id} ----

func TestOpenAPI_GetTask_200(t *testing.T) {
	srv := testServer(t)
	id := createTestTask(t, srv)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+id, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	checkResponse(t, req, rec)
}

func TestOpenAPI_GetTask_404(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/nonexistent-id", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	checkResponse(t, req, rec)
}

// ---- DELETE /api/v1/tasks/{id} ----

func TestOpenAPI_DeleteTask_204(t *testing.T) {
	srv := testServer(t)
	id := createTestTask(t, srv)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/tasks/"+id, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	checkResponse(t, req, rec)
}

func TestOpenAPI_DeleteTask_404(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/tasks/nonexistent-id", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	checkResponse(t, req, rec)
}

// ---- GET /api/v1/tasks/{id}/logs ----

func TestOpenAPI_GetTaskLogs_200_NotRunning(t *testing.T) {
	srv := testServer(t)
	id := createTestTask(t, srv)

	// Task not yet running → returns a status text line (200 text/plain).
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+id+"/logs", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	checkResponse(t, req, rec)
}

func TestOpenAPI_GetTaskLogs_404(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/nonexistent-id/logs", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	checkResponse(t, req, rec)
}

// ---- GET /api/v1/readings ----

func TestOpenAPI_ListReadings_200_Empty(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/readings?limit=20&offset=0", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	requireSpecRoute(t, req)
	checkResponse(t, req, rec)
}

// ---- GET /api/v1/readings/{id} ----

func TestOpenAPI_GetReading_404(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/readings/bf_READ_MISSING", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	requireSpecRoute(t, req)
	checkResponse(t, req, rec)
}
