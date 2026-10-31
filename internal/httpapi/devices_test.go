package httpapi_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/erikmatioli/devices-api/internal/app"
	"github.com/erikmatioli/devices-api/internal/app/apptest"
	"github.com/erikmatioli/devices-api/internal/auth"
	"github.com/erikmatioli/devices-api/internal/httpapi"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

const (
	testIssuer   = "https://issuer.test"
	testAudience = "devices-api"
)

// apiFixture runs requests through the full router with the real service,
// an in-memory repository and the real token verifier. Requests carry a
// token with every scope unless they set Authorization themselves.
type apiFixture struct {
	t       *testing.T
	repo    *apptest.MemoryRepository
	handler http.Handler
}

func newAPI(t *testing.T) *apiFixture {
	t.Helper()
	repo := apptest.NewMemoryRepository()
	// Each call advances the clock so creation order is deterministic.
	now := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		now = now.Add(time.Second)
		return now
	}
	svc := app.NewService(repo, app.WithClock(clock))
	verifier := auth.NewHS256Verifier(auth.Config{
		Issuer: testIssuer, Audience: testAudience, HS256Secret: testSecret, ClockSkew: time.Second,
	})
	return &apiFixture{
		t:    t,
		repo: repo,
		handler: httpapi.NewRouter(httpapi.Dependencies{
			Logger:   discardLogger(),
			Devices:  svc,
			Verifier: verifier,
		}),
	}
}

// token mints a valid access token with the given scopes.
func (f *apiFixture) token(scopes ...string) string {
	f.t.Helper()
	tok, err := auth.SignHS256(testSecret, testIssuer, testAudience, "test-user", scopes, time.Hour, time.Now())
	if err != nil {
		f.t.Fatal(err)
	}
	return tok
}

type response struct {
	*httptest.ResponseRecorder
}

func (r response) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body.Bytes(), v); err != nil {
		t.Fatalf("decode body %q: %v", r.Body.String(), err)
	}
}

func (r response) problem(t *testing.T) problemBody {
	t.Helper()
	if ct := r.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json (body %q)", ct, r.Body.String())
	}
	var p problemBody
	r.json(t, &p)
	return p
}

type problemBody struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail"`
	Instance string `json:"instance"`
	Errors   []struct {
		Field string `json:"field"`
		Code  string `json:"code"`
	} `json:"errors"`
}

func (p problemBody) codes() string {
	parts := make([]string, len(p.Errors))
	for i, e := range p.Errors {
		parts[i] = e.Field + ":" + e.Code
	}
	return strings.Join(parts, ",")
}

type deviceBody struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Brand        string `json:"brand"`
	State        string `json:"state"`
	CreationTime string `json:"creation_time"`
}

type listBody struct {
	Data       []deviceBody `json:"data"`
	NextCursor *string      `json:"next_cursor"`
}

func (f *apiFixture) do(method, path, body string, headers ...string) response {
	f.t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+f.token(auth.ScopeRead, auth.ScopeWrite, auth.ScopeDelete))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return response{rec}
}

func (f *apiFixture) create(body string) deviceBody {
	f.t.Helper()
	res := f.do(http.MethodPost, "/v1/devices", body)
	if res.Code != http.StatusCreated {
		f.t.Fatalf("create %s: status %d, body %s", body, res.Code, res.Body.String())
	}
	var d deviceBody
	res.json(f.t, &d)
	return d
}

func TestCreateDevice(t *testing.T) {
	api := newAPI(t)

	res := api.do(http.MethodPost, "/v1/devices", `{"name":"  Pixel 9 ","brand":"Google"}`)

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", res.Code, res.Body.String())
	}
	var d deviceBody
	res.json(t, &d)
	if d.Name != "Pixel 9" || d.Brand != "Google" || d.State != "available" || d.ID == "" {
		t.Errorf("body = %+v", d)
	}
	if _, err := time.Parse(time.RFC3339Nano, d.CreationTime); err != nil {
		t.Errorf("creation_time %q is not RFC 3339: %v", d.CreationTime, err)
	}
	if loc := res.Header().Get("Location"); loc != "/v1/devices/"+d.ID {
		t.Errorf("Location = %q", loc)
	}
	if etag := res.Header().Get("ETag"); etag != `"1"` {
		t.Errorf("ETag = %q, want \"1\"", etag)
	}
	if res.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing X-Content-Type-Options header")
	}
}

func TestCreateDeviceWithState(t *testing.T) {
	d := newAPI(t).create(`{"name":"Pixel","brand":"Google","state":"in-use"}`)
	if d.State != "in-use" {
		t.Errorf("state = %q, want in-use", d.State)
	}
}

func TestCreateDeviceValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "missing fields", body: `{}`, want: "name:required,brand:required"},
		{name: "empty after trim", body: `{"name":"  ","brand":"Google"}`, want: "name:required"},
		{name: "wrong types", body: `{"name":42,"brand":true}`, want: "name:invalid_type,brand:invalid_type"},
		{name: "null values", body: `{"name":null,"brand":"Google"}`, want: "name:invalid_value"},
		{name: "shape and value errors together", body: `{"name":"","brand":7,"color":"red"}`, want: "brand:invalid_type,name:required,color:unknown_field"},
		{name: "too long", body: `{"name":"` + strings.Repeat("x", 101) + `","brand":"Google"}`, want: "name:too_long"},
		{name: "unknown state", body: `{"name":"Pixel","brand":"Google","state":"broken"}`, want: "state:invalid_value"},
		{name: "read-only fields", body: `{"id":"x","creation_time":"2026-01-01T00:00:00Z","name":"Pixel","brand":"Google"}`, want: "id:read_only,creation_time:read_only"},
		{name: "unknown fields", body: `{"name":"Pixel","brand":"Google","serial":"1","color":"red"}`, want: "color:unknown_field,serial:unknown_field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := newAPI(t).do(http.MethodPost, "/v1/devices", tt.body)
			if res.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (body %s)", res.Code, res.Body.String())
			}
			p := res.problem(t)
			if p.codes() != tt.want {
				t.Errorf("errors = %s, want %s", p.codes(), tt.want)
			}
			if p.Type != "/problems/validation-error" || p.Instance != "/v1/devices" {
				t.Errorf("problem = %+v", p)
			}
		})
	}
}

func TestCreateDeviceMalformedBody(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
	}{
		{name: "invalid json", body: `{"name":`, wantStatus: http.StatusBadRequest},
		{name: "empty body", body: ``, wantStatus: http.StatusBadRequest},
		{name: "array body", body: `[{"name":"x"}]`, wantStatus: http.StatusBadRequest},
		{name: "null body", body: `null`, wantStatus: http.StatusBadRequest},
		{name: "trailing data", body: `{"name":"a","brand":"b"} {}`, wantStatus: http.StatusBadRequest},
		{name: "too large", body: `{"name":"` + strings.Repeat("x", 1<<20) + `"}`, wantStatus: http.StatusRequestEntityTooLarge},
		{name: "wrong content type", body: `{"name":"a","brand":"b"}`, contentType: "text/plain", wantStatus: http.StatusUnsupportedMediaType},
		{name: "missing content type", body: `{"name":"a","brand":"b"}`, contentType: "-", wantStatus: http.StatusUnsupportedMediaType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/devices", strings.NewReader(tt.body))
			switch tt.contentType {
			case "":
				req.Header.Set("Content-Type", "application/json; charset=utf-8")
			case "-":
			default:
				req.Header.Set("Content-Type", tt.contentType)
			}
			api := newAPI(t)
			req.Header.Set("Authorization", "Bearer "+api.token(auth.ScopeWrite))
			rec := httptest.NewRecorder()
			api.handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			response{rec}.problem(t)
		})
	}
}

func TestGetDevice(t *testing.T) {
	api := newAPI(t)
	created := api.create(`{"name":"Pixel","brand":"Google"}`)

	res := api.do(http.MethodGet, "/v1/devices/"+created.ID, "")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}
	var got deviceBody
	res.json(t, &got)
	if got != created {
		t.Errorf("got %+v, want %+v", got, created)
	}

	res = api.do(http.MethodGet, "/v1/devices/"+created.ID, "", "If-None-Match", `"1"`)
	if res.Code != http.StatusNotModified || res.Body.Len() != 0 {
		t.Errorf("conditional GET: status %d, body %q, want 304 with no body", res.Code, res.Body.String())
	}
}

func TestGetDeviceErrors(t *testing.T) {
	api := newAPI(t)

	res := api.do(http.MethodGet, "/v1/devices/0192f7a1-1d3b-7e55-8c11-2a9f4b6c0d12", "")
	if res.Code != http.StatusNotFound || res.problem(t).Type != "/problems/device-not-found" {
		t.Errorf("missing device: status %d, body %s", res.Code, res.Body.String())
	}

	res = api.do(http.MethodGet, "/v1/devices/not-a-uuid", "")
	if res.Code != http.StatusUnprocessableEntity || res.problem(t).codes() != "id:invalid_value" {
		t.Errorf("invalid id: status %d, body %s", res.Code, res.Body.String())
	}
}

func TestListDevices(t *testing.T) {
	api := newAPI(t)
	api.create(`{"name":"d1","brand":"Google"}`)
	api.create(`{"name":"d2","brand":"Apple","state":"in-use"}`)
	api.create(`{"name":"d3","brand":"google","state":"in-use"}`)
	api.create(`{"name":"d4","brand":"Samsung","state":"inactive"}`)

	tests := []struct {
		query string
		want  string
	}{
		{query: "", want: "d1,d2,d3,d4"},
		{query: "?brand=GOOGLE", want: "d1,d3"},
		{query: "?state=in-use", want: "d2,d3"},
		{query: "?brand=google&state=in-use", want: "d3"},
		{query: "?brand=Nokia", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			res := api.do(http.MethodGet, "/v1/devices"+tt.query, "")
			if res.Code != http.StatusOK {
				t.Fatalf("status = %d, body %s", res.Code, res.Body.String())
			}
			if !strings.Contains(res.Body.String(), `"data":[`) {
				t.Errorf("data must be an array even when empty: %s", res.Body.String())
			}
			var body listBody
			res.json(t, &body)
			names := make([]string, len(body.Data))
			for i, d := range body.Data {
				names[i] = d.Name
			}
			if got := strings.Join(names, ","); got != tt.want {
				t.Errorf("devices = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestListDevicesPagination(t *testing.T) {
	api := newAPI(t)
	for i := range 5 {
		api.create(`{"name":"d` + strconv.Itoa(i) + `","brand":"Brand"}`)
	}

	var names []string
	path := "/v1/devices?limit=2"
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("pagination does not terminate")
		}
		res := api.do(http.MethodGet, path, "")
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, body %s", res.Code, res.Body.String())
		}
		var body listBody
		res.json(t, &body)
		for _, d := range body.Data {
			names = append(names, d.Name)
		}
		if body.NextCursor == nil {
			break
		}
		path = "/v1/devices?limit=2&cursor=" + *body.NextCursor
	}
	if got := strings.Join(names, ","); got != "d0,d1,d2,d3,d4" {
		t.Errorf("paginated devices = %s", got)
	}
}

func TestListDevicesInvalidQuery(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{query: "?limit=0", want: "limit:invalid_value"},
		{query: "?limit=101", want: "limit:invalid_value"},
		{query: "?limit=ten", want: "limit:invalid_value"},
		{query: "?state=busy", want: "state:invalid_value"},
		{query: "?brand=" + strings.Repeat("b", 51), want: "brand:too_long"},
		{query: "?cursor=not*base64", want: "cursor:invalid_value"},
		{query: "?cursor=c2hvcnQ", want: "cursor:invalid_value"},
		{query: "?state=busy&limit=-1", want: "state:invalid_value,limit:invalid_value"},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			res := newAPI(t).do(http.MethodGet, "/v1/devices"+tt.query, "")
			if res.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", res.Code)
			}
			if got := res.problem(t).codes(); got != tt.want {
				t.Errorf("errors = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestReplaceDevice(t *testing.T) {
	api := newAPI(t)
	d := api.create(`{"name":"Pixel","brand":"Google"}`)

	res := api.do(http.MethodPut, "/v1/devices/"+d.ID,
		`{"id":"`+d.ID+`","name":"Pixel 2","brand":"Alphabet","state":"inactive","creation_time":"`+d.CreationTime+`"}`)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", res.Code, res.Body.String())
	}
	var got deviceBody
	res.json(t, &got)
	if got.Name != "Pixel 2" || got.Brand != "Alphabet" || got.State != "inactive" || got.CreationTime != d.CreationTime {
		t.Errorf("updated = %+v", got)
	}
	if etag := res.Header().Get("ETag"); etag != `"2"` {
		t.Errorf("ETag = %q, want \"2\"", etag)
	}
}

func TestReplaceDeviceErrors(t *testing.T) {
	tests := []struct {
		name       string
		start      string
		body       string
		headers    []string
		wantStatus int
		wantType   string
		wantCodes  string
	}{
		{
			name:       "missing state",
			start:      "available",
			body:       `{"name":"Pixel","brand":"Google"}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantCodes:  "state:required",
		},
		{
			name:       "different creation time",
			start:      "available",
			body:       `{"name":"Pixel","brand":"Google","state":"available","creation_time":"2020-01-01T00:00:00Z"}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantCodes:  "creation_time:read_only",
		},
		{
			name:       "malformed creation time",
			start:      "available",
			body:       `{"name":"Pixel","brand":"Google","state":"available","creation_time":"yesterday"}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantCodes:  "creation_time:invalid_value",
		},
		{
			name:       "different id",
			start:      "available",
			body:       `{"id":"0192f7a1-1d3b-7e55-8c11-2a9f4b6c0d12","name":"Pixel","brand":"Google","state":"available"}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantCodes:  "id:read_only",
		},
		{
			name:       "rename while in use",
			start:      "in-use",
			body:       `{"name":"Renamed","brand":"Google","state":"in-use"}`,
			wantStatus: http.StatusConflict,
			wantType:   "/problems/device-in-use",
		},
		{
			name:       "stale If-Match",
			start:      "available",
			body:       `{"name":"Pixel","brand":"Google","state":"inactive"}`,
			headers:    []string{"If-Match", `"9"`},
			wantStatus: http.StatusPreconditionFailed,
			wantType:   "/problems/version-mismatch",
		},
		{
			name:       "weak If-Match",
			start:      "available",
			body:       `{"name":"Pixel","brand":"Google","state":"inactive"}`,
			headers:    []string{"If-Match", `W/"1"`},
			wantStatus: http.StatusPreconditionFailed,
			wantType:   "/problems/version-mismatch",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newAPI(t)
			d := api.create(`{"name":"Pixel","brand":"Google","state":"` + tt.start + `"}`)

			res := api.do(http.MethodPut, "/v1/devices/"+d.ID, tt.body, tt.headers...)

			if res.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", res.Code, tt.wantStatus, res.Body.String())
			}
			p := res.problem(t)
			if tt.wantType != "" && p.Type != tt.wantType {
				t.Errorf("type = %s, want %s", p.Type, tt.wantType)
			}
			if tt.wantCodes != "" && p.codes() != tt.wantCodes {
				t.Errorf("errors = %s, want %s", p.codes(), tt.wantCodes)
			}

			// The stored device is untouched.
			var stored deviceBody
			api.do(http.MethodGet, "/v1/devices/"+d.ID, "").json(t, &stored)
			if stored != d {
				t.Errorf("device changed after a rejected update: %+v", stored)
			}
		})
	}
}

func TestReplaceDeviceWithMatchingIfMatch(t *testing.T) {
	api := newAPI(t)
	d := api.create(`{"name":"Pixel","brand":"Google"}`)

	for _, etag := range []string{`"1"`, "*"} {
		res := api.do(http.MethodPut, "/v1/devices/"+d.ID, `{"name":"Pixel","brand":"Google","state":"in-use"}`, "If-Match", etag)
		if res.Code != http.StatusOK {
			t.Errorf("If-Match %s: status %d, body %s", etag, res.Code, res.Body.String())
		}
	}
}

func TestPatchDevice(t *testing.T) {
	api := newAPI(t)
	d := api.create(`{"name":"Pixel","brand":"Google"}`)

	req := httptest.NewRequest(http.MethodPatch, "/v1/devices/"+d.ID, strings.NewReader(`{"state":"in-use"}`))
	req.Header.Set("Content-Type", "application/merge-patch+json")
	req.Header.Set("Authorization", "Bearer "+api.token(auth.ScopeWrite))
	rec := httptest.NewRecorder()
	api.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var got deviceBody
	response{rec}.json(t, &got)
	if got.State != "in-use" || got.Name != "Pixel" || got.Brand != "Google" {
		t.Errorf("patched = %+v", got)
	}

	res := api.do(http.MethodPatch, "/v1/devices/"+d.ID, `{"brand":"Other"}`)
	if res.Code != http.StatusConflict {
		t.Errorf("brand change while in use: status %d, want 409", res.Code)
	}
}

func TestPatchDeviceEdgeCases(t *testing.T) {
	api := newAPI(t)
	d := api.create(`{"name":"Pixel","brand":"Google"}`)

	res := api.do(http.MethodPatch, "/v1/devices/"+d.ID, `{}`)
	if res.Code != http.StatusOK || res.Header().Get("ETag") != `"1"` {
		t.Errorf("empty patch: status %d, ETag %s, want 200 and unchanged version", res.Code, res.Header().Get("ETag"))
	}

	res = api.do(http.MethodPatch, "/v1/devices/"+d.ID, `{"name":null}`)
	if res.Code != http.StatusUnprocessableEntity || res.problem(t).codes() != "name:invalid_value" {
		t.Errorf("null name: status %d, body %s", res.Code, res.Body.String())
	}

	res = api.do(http.MethodPatch, "/v1/devices/0192f7a1-1d3b-7e55-8c11-2a9f4b6c0d12", `{"name":"x"}`)
	if res.Code != http.StatusNotFound {
		t.Errorf("missing device: status %d, want 404", res.Code)
	}
}

func TestDeleteDevice(t *testing.T) {
	api := newAPI(t)
	free := api.create(`{"name":"Free","brand":"Google"}`)
	busy := api.create(`{"name":"Busy","brand":"Google","state":"in-use"}`)

	if res := api.do(http.MethodDelete, "/v1/devices/"+free.ID, ""); res.Code != http.StatusNoContent || res.Body.Len() != 0 {
		t.Errorf("delete available: status %d, body %q", res.Code, res.Body.String())
	}
	if res := api.do(http.MethodGet, "/v1/devices/"+free.ID, ""); res.Code != http.StatusNotFound {
		t.Errorf("deleted device still readable: %d", res.Code)
	}

	res := api.do(http.MethodDelete, "/v1/devices/"+busy.ID, "")
	if res.Code != http.StatusConflict {
		t.Fatalf("delete in-use: status %d, want 409", res.Code)
	}
	if p := res.problem(t); !strings.Contains(p.Detail, "cannot be deleted") {
		t.Errorf("detail = %q", p.Detail)
	}

	if res := api.do(http.MethodDelete, "/v1/devices/"+free.ID, ""); res.Code != http.StatusNotFound {
		t.Errorf("delete twice: status %d, want 404", res.Code)
	}
}

func TestInternalErrorsDoNotLeak(t *testing.T) {
	api := newAPI(t)
	api.repo.Err = errors.New("pq: password authentication failed for user admin")

	res := api.do(http.MethodGet, "/v1/devices", "")

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", res.Code)
	}
	if strings.Contains(res.Body.String(), "password") {
		t.Errorf("internal error leaked: %s", res.Body.String())
	}
	if res.problem(t).Type != "/problems/internal-error" {
		t.Errorf("unexpected problem %s", res.Body.String())
	}
}

func TestUnknownRouteAndMethod(t *testing.T) {
	api := newAPI(t)

	res := api.do(http.MethodGet, "/v2/things", "")
	if res.Code != http.StatusNotFound || res.problem(t).Type != "/problems/not-found" {
		t.Errorf("unknown route: status %d, body %s", res.Code, res.Body.String())
	}

	res = api.do(http.MethodPost, "/v1/devices/0192f7a1-1d3b-7e55-8c11-2a9f4b6c0d12", `{}`)
	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method: status %d, want 405", res.Code)
	}
	if res.problem(t).Type != "/problems/method-not-allowed" {
		t.Errorf("unexpected problem %s", res.Body.String())
	}
	allow := res.Header().Get("Allow")
	for _, m := range []string{"GET", "PUT", "PATCH", "DELETE"} {
		if !strings.Contains(allow, m) {
			t.Errorf("Allow = %q, missing %s", allow, m)
		}
	}
}

func TestDocumentation(t *testing.T) {
	api := newAPI(t)

	res := api.do(http.MethodGet, "/openapi.yaml", "")
	if res.Code != http.StatusOK || !strings.HasPrefix(res.Body.String(), "openapi: 3.1.0") {
		t.Errorf("openapi.yaml: status %d", res.Code)
	}
	if ct := res.Header().Get("Content-Type"); ct != "application/yaml" {
		t.Errorf("openapi.yaml Content-Type = %q", ct)
	}

	res = api.do(http.MethodGet, "/docs", "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "/openapi.yaml") {
		t.Errorf("docs: status %d", res.Code)
	}
}
