package api

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	yaml "go.yaml.in/yaml/v3"
)

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestSwaggerUIIsServed(t *testing.T) {
	h, _ := newServer(t, &fakeAPI{})

	w := get(t, h, "/api/docs")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(w.Body.String(), "swagger-ui") {
		t.Error("the page does not reference swagger-ui")
	}
}

func TestDocsStayOpenWhenATokenIsConfigured(t *testing.T) {
	h := newGuardedServer(t, testToken)

	for _, path := range []string{
		"/api/docs",
		"/api/docs/openapi.yaml",
		"/api/docs/swagger-ui.css",
		"/api/docs/swagger-ui-bundle.js",
	} {
		if w := get(t, h, path); w.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200 without a token", path, w.Code)
		}
	}
}

func TestOpenAPISpecIsServedAndParses(t *testing.T) {
	h, _ := newServer(t, &fakeAPI{})

	w := get(t, h, "/api/docs/openapi.yaml")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "yaml") {
		t.Errorf("Content-Type = %q, want a yaml type", ct)
	}

	var spec struct {
		OpenAPI string `yaml:"openapi"`
		Info    struct {
			Title string `yaml:"title"`
		} `yaml:"info"`
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(w.Body.Bytes(), &spec); err != nil {
		t.Fatalf("the served spec is not valid yaml: %v", err)
	}
	if !strings.HasPrefix(spec.OpenAPI, "3.") {
		t.Errorf("openapi = %q, want a 3.x version", spec.OpenAPI)
	}
	if spec.Info.Title == "" {
		t.Error("info.title is empty")
	}
}

func TestSwaggerAssetsAreServedWithTheRightTypes(t *testing.T) {
	h, _ := newServer(t, &fakeAPI{})

	for path, wantType := range map[string]string{
		"/api/docs/swagger-ui.css":       "text/css",
		"/api/docs/swagger-ui-bundle.js": "javascript",
	} {
		w := get(t, h, path)
		if w.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", path, w.Code)
			continue
		}
		if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, wantType) {
			t.Errorf("%s Content-Type = %q, want %q", path, ct, wantType)
		}
		if w.Body.Len() == 0 {
			t.Errorf("%s served an empty body", path)
		}
	}
}

// internalRoutes are registered but deliberately absent from openapi.yaml.
//
// They are machine-to-machine plumbing between cubit and the cubit-login
// helper, not endpoints anyone should call by hand — and publishing them in
// Swagger actively misled: /auth/browser reads like a login button, but it only
// arms cubit to receive a code and sends no SMS, so calling it just wedges the
// state machine. /auth/login is here for a different reason: Pluxee's reCAPTCHA
// means it can now only ever answer 412.
//
// The list is explicit so the drift test stays strict: a new route must be
// either documented or named here on purpose.
var internalRoutes = map[string]string{
	"POST /api/v1/auth/browser":      "helper arms the otp relay",
	"GET /api/v1/auth/browser/otp":   "helper collects the parked code",
	"GET /api/v1/auth/login-request": "helper collects a pending login",
	"POST /api/v1/auth/session":      "helper hands over a captured session",
	"POST /api/v1/auth/login":        "unreachable: pluxee requires a captcha token",
}

// TestInternalRoutesStillExist guards the list above from rotting: an entry for
// a route that no longer exists would silently weaken the drift test.
func TestInternalRoutesStillExist(t *testing.T) {
	h, _ := newServer(t, &fakeAPI{})
	routes, ok := h.(chi.Routes)
	if !ok {
		t.Fatal("the handler does not expose its routes")
	}

	registered := map[string]bool{}
	_ = chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		registered[method+" "+strings.TrimSuffix(route, "/*")] = true
		return nil
	})
	for r, why := range internalRoutes {
		if !registered[r] {
			t.Errorf("internalRoutes lists %q (%s) but no such route is registered", r, why)
		}
	}
}

// TestSpecMatchesTheRegisteredRoutes is the test that stops the spec rotting:
// it fails when a route is added without documenting it, or documented without
// existing. The docs routes themselves are excluded — a spec that documents its
// own viewer is noise.
func TestSpecMatchesTheRegisteredRoutes(t *testing.T) {
	h, _ := newServer(t, &fakeAPI{})

	w := get(t, h, "/api/docs/openapi.yaml")
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(w.Body.Bytes(), &spec); err != nil {
		t.Fatalf("parsing the spec: %v", err)
	}

	// A path item holds operations keyed by http method, plus non-method keys
	// such as "parameters" and "summary". Only the methods are routes.
	httpMethods := map[string]bool{
		"get": true, "put": true, "post": true, "delete": true,
		"options": true, "head": true, "patch": true, "trace": true,
	}
	documented := map[string]bool{}
	for path, ops := range spec.Paths {
		for method := range ops {
			if !httpMethods[strings.ToLower(method)] {
				continue
			}
			documented[strings.ToUpper(method)+" "+path] = true
		}
	}

	registered := map[string]bool{}
	routes, ok := h.(chi.Routes)
	if !ok {
		t.Fatal("the handler does not expose its routes")
	}
	err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(route, "/*")
		if route == "" {
			route = "/"
		}
		if strings.HasPrefix(route, "/api/docs") {
			return nil
		}
		if _, internal := internalRoutes[method+" "+route]; internal {
			return nil
		}
		registered[method+" "+route] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walking routes: %v", err)
	}

	var undocumented, phantom []string
	for r := range registered {
		if !documented[r] {
			undocumented = append(undocumented, r)
		}
	}
	for d := range documented {
		if !registered[d] {
			phantom = append(phantom, d)
		}
	}
	sort.Strings(undocumented)
	sort.Strings(phantom)

	if len(undocumented) > 0 {
		t.Errorf("routes exist but are not in openapi.yaml: %v", undocumented)
	}
	if len(phantom) > 0 {
		t.Errorf("openapi.yaml documents routes that do not exist: %v", phantom)
	}
}
