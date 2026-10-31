package httpapi_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/erikmatioli/devices-api/internal/auth"
)

func TestAuthenticationRequired(t *testing.T) {
	expired, err := auth.SignHS256(testSecret, testIssuer, testAudience, "alice",
		[]string{auth.ScopeRead}, time.Minute, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name          string
		authorization string
		wantChallenge string
	}{
		{name: "missing header", authorization: "", wantChallenge: `Bearer realm="devices-api"`},
		{name: "basic scheme", authorization: "Basic YWxpY2U6c2VjcmV0", wantChallenge: `Bearer realm="devices-api"`},
		{name: "empty bearer", authorization: "Bearer ", wantChallenge: `Bearer realm="devices-api"`},
		{name: "garbage token", authorization: "Bearer abc.def.ghi", wantChallenge: `error="invalid_token"`},
		{name: "expired token", authorization: "Bearer " + expired, wantChallenge: `error="invalid_token"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := newAPI(t).do(http.MethodGet, "/v1/devices", "", "Authorization", tt.authorization)

			if res.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", res.Code)
			}
			if got := res.Header().Get("WWW-Authenticate"); !strings.Contains(got, tt.wantChallenge) {
				t.Errorf("WWW-Authenticate = %q, want it to contain %q", got, tt.wantChallenge)
			}
			if p := res.problem(t); p.Type != "/problems/unauthorized" || strings.Contains(p.Detail, "expired by") {
				t.Errorf("problem = %+v", p)
			}
		})
	}
}

func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	api := newAPI(t)
	res := api.do(http.MethodGet, "/v1/devices", "", "Authorization", "bearer "+api.token(auth.ScopeRead))
	if res.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", res.Code)
	}
}

// TestRouteScopes checks every protected route against a token that has all
// scopes except the one the route needs.
func TestRouteScopes(t *testing.T) {
	const id = "0192f7a1-1d3b-7e55-8c11-2a9f4b6c0d12"
	routes := []struct {
		method string
		path   string
		scope  string
	}{
		{http.MethodPost, "/v1/devices", auth.ScopeWrite},
		{http.MethodGet, "/v1/devices", auth.ScopeRead},
		{http.MethodGet, "/v1/devices/" + id, auth.ScopeRead},
		{http.MethodPut, "/v1/devices/" + id, auth.ScopeWrite},
		{http.MethodPatch, "/v1/devices/" + id, auth.ScopeWrite},
		{http.MethodDelete, "/v1/devices/" + id, auth.ScopeDelete},
		{http.MethodGet, "/v1/devices/events?device_id=invalid", auth.ScopeRead},
	}
	all := []string{auth.ScopeRead, auth.ScopeWrite, auth.ScopeDelete}

	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			api := newAPI(t)
			var others []string
			for _, s := range all {
				if s != rt.scope {
					others = append(others, s)
				}
			}

			res := api.do(rt.method, rt.path, `{}`, "Authorization", "Bearer "+api.token(others...))

			if res.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", res.Code)
			}
			want := `error="insufficient_scope", scope="` + rt.scope + `"`
			if got := res.Header().Get("WWW-Authenticate"); !strings.Contains(got, want) {
				t.Errorf("WWW-Authenticate = %q, want %q", got, want)
			}
			if p := res.problem(t); p.Type != "/problems/forbidden" {
				t.Errorf("problem = %+v", p)
			}

			res = api.do(rt.method, rt.path, `{}`, "Authorization", "Bearer "+api.token(rt.scope))
			if res.Code == http.StatusForbidden || res.Code == http.StatusUnauthorized {
				t.Errorf("token with %s: status %d", rt.scope, res.Code)
			}
		})
	}
}

func TestPublicRoutesNeedNoToken(t *testing.T) {
	api := newAPI(t)
	for _, path := range []string{"/healthz", "/readyz", "/openapi.yaml", "/docs"} {
		if res := api.do(http.MethodGet, path, "", "Authorization", ""); res.Code != http.StatusOK {
			t.Errorf("%s: status %d, want 200", path, res.Code)
		}
	}
}
