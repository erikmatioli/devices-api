package httpapi

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/erikmatioli/devices-api/internal/auth"
)

// TokenVerifier validates a bearer token and returns its principal.
type TokenVerifier interface {
	Verify(token string) (auth.Principal, error)
}

const problemUnauthorized = "/problems/unauthorized"
const problemForbidden = "/problems/forbidden"

// authenticate requires a valid bearer token (RFC 6750) and stores the
// principal in the request context. The rejection reason is logged but
// never returned, so clients cannot probe why a token failed.
func authenticate(v TokenVerifier, logger *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearerToken(r)
			if !ok {
				w.Header().Set("WWW-Authenticate", `Bearer realm="devices-api"`)
				writeProblem(w, r, http.StatusUnauthorized, problemUnauthorized, "Unauthorized",
					"a bearer access token is required")
				return
			}
			p, err := v.Verify(raw)
			if err != nil {
				logger.InfoContext(r.Context(), "access token rejected",
					slog.String("request_id", RequestIDFrom(r.Context())), slog.Any("reason", err))
				w.Header().Set("WWW-Authenticate", `Bearer realm="devices-api", error="invalid_token"`)
				writeProblem(w, r, http.StatusUnauthorized, problemUnauthorized, "Unauthorized",
					"the access token is invalid or expired")
				return
			}
			if meta := requestMetaFrom(r.Context()); meta != nil {
				meta.subject = p.Subject
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	}
}

// requireScope lets the request through only when the authenticated
// principal holds scope.
func requireScope(scope string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.PrincipalFrom(r.Context())
		if !ok || !p.HasScope(scope) {
			w.Header().Set("WWW-Authenticate",
				`Bearer realm="devices-api", error="insufficient_scope", scope="`+scope+`"`)
			writeProblem(w, r, http.StatusForbidden, problemForbidden, "Forbidden",
				"the access token lacks the "+scope+" scope")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerToken extracts the token from "Authorization: Bearer <token>". The
// scheme is case insensitive as required by RFC 7235.
func bearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}
