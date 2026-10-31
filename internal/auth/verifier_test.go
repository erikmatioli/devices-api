package auth_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/erikmatioli/devices-api/internal/auth"
)

const (
	issuer   = "https://issuer.test"
	audience = "devices-api"
)

var secret = []byte("0123456789abcdef0123456789abcdef")

func hsConfig() auth.Config {
	return auth.Config{Issuer: issuer, Audience: audience, HS256Secret: secret, ClockSkew: 30 * time.Second}
}

func validClaims() auth.Claims {
	now := time.Now()
	return auth.Claims{
		Scope: "devices:read devices:write",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			Subject:   "alice",
			Audience:  jwt.ClaimStrings{audience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	}
}

func sign(t *testing.T, method jwt.SigningMethod, key any, claims auth.Claims, kid string) string {
	t.Helper()
	tok := jwt.NewWithClaims(method, claims)
	if kid != "" {
		tok.Header["kid"] = kid
	}
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

func TestHS256Verify(t *testing.T) {
	v := auth.NewHS256Verifier(hsConfig())

	p, err := v.Verify(sign(t, jwt.SigningMethodHS256, secret, validClaims(), ""))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if p.Subject != "alice" || !p.HasScope(auth.ScopeRead) || !p.HasScope(auth.ScopeWrite) || p.HasScope(auth.ScopeDelete) {
		t.Errorf("principal = %+v", p)
	}
}

func TestVerifyScpArrayClaim(t *testing.T) {
	c := validClaims()
	c.Scope = ""
	c.Scp = []string{auth.ScopeDelete}

	p, err := auth.NewHS256Verifier(hsConfig()).Verify(sign(t, jwt.SigningMethodHS256, secret, c, ""))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !slices.Equal(p.Scopes, []string{auth.ScopeDelete}) {
		t.Errorf("scopes = %v", p.Scopes)
	}
}

func TestHS256Rejects(t *testing.T) {
	past := time.Now().Add(-2 * time.Hour)
	future := time.Now().Add(2 * time.Hour)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		token func() string
	}{
		{name: "garbage", token: func() string { return "not.a.jwt" }},
		{name: "wrong secret", token: func() string {
			return sign(t, jwt.SigningMethodHS256, []byte("another-secret-another-secret-xx"), validClaims(), "")
		}},
		{name: "expired", token: func() string {
			c := validClaims()
			c.ExpiresAt = jwt.NewNumericDate(past)
			return sign(t, jwt.SigningMethodHS256, secret, c, "")
		}},
		{name: "missing expiry", token: func() string {
			c := validClaims()
			c.ExpiresAt = nil
			return sign(t, jwt.SigningMethodHS256, secret, c, "")
		}},
		{name: "not yet valid", token: func() string {
			c := validClaims()
			c.NotBefore = jwt.NewNumericDate(future)
			return sign(t, jwt.SigningMethodHS256, secret, c, "")
		}},
		{name: "issued in the future", token: func() string {
			c := validClaims()
			c.IssuedAt = jwt.NewNumericDate(future)
			return sign(t, jwt.SigningMethodHS256, secret, c, "")
		}},
		{name: "wrong issuer", token: func() string {
			c := validClaims()
			c.Issuer = "https://evil.test"
			return sign(t, jwt.SigningMethodHS256, secret, c, "")
		}},
		{name: "wrong audience", token: func() string {
			c := validClaims()
			c.Audience = jwt.ClaimStrings{"other-api"}
			return sign(t, jwt.SigningMethodHS256, secret, c, "")
		}},
		{name: "missing subject", token: func() string {
			c := validClaims()
			c.Subject = ""
			return sign(t, jwt.SigningMethodHS256, secret, c, "")
		}},
		{name: "alg none", token: func() string {
			return sign(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, validClaims(), "")
		}},
		{name: "unexpected algorithm", token: func() string {
			return sign(t, jwt.SigningMethodRS256, rsaKey, validClaims(), "")
		}},
		{name: "HS384 instead of HS256", token: func() string {
			return sign(t, jwt.SigningMethodHS384, secret, validClaims(), "")
		}},
	}

	v := auth.NewHS256Verifier(hsConfig())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := v.Verify(tt.token()); !errors.Is(err, auth.ErrInvalidToken) {
				t.Errorf("error = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestClockSkewIsTolerated(t *testing.T) {
	c := validClaims()
	c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-10 * time.Second))

	if _, err := auth.NewHS256Verifier(hsConfig()).Verify(sign(t, jwt.SigningMethodHS256, secret, c, "")); err != nil {
		t.Errorf("token expired 10s ago should pass with 30s skew: %v", err)
	}
}

func TestSignHS256RoundTrip(t *testing.T) {
	tok, err := auth.SignHS256(secret, issuer, audience, "bob", []string{auth.ScopeRead}, time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p, err := auth.NewHS256Verifier(hsConfig()).Verify(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if p.Subject != "bob" || !slices.Equal(p.Scopes, []string{auth.ScopeRead}) {
		t.Errorf("principal = %+v", p)
	}
}

// jwksServer publishes the public halves of the given keys.
func jwksServer(t *testing.T, rsaKey *rsa.PublicKey, ecKey *ecdsa.PublicKey) *httptest.Server {
	t.Helper()
	b64 := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	ecBytes, err := ecKey.Bytes() // uncompressed point: 0x04 || X || Y
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]any{"keys": []map[string]string{
		{"kty": "RSA", "kid": "rsa-1", "use": "sig", "alg": "RS256",
			"n": b64(rsaKey.N.Bytes()), "e": b64(big.NewInt(int64(rsaKey.E)).Bytes())},
		{"kty": "EC", "kid": "ec-1", "use": "sig", "alg": "ES256", "crv": "P-256",
			"x": b64(ecBytes[1:33]), "y": b64(ecBytes[33:])},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestJWKSVerify(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	srv := jwksServer(t, &rsaKey.PublicKey, &ecKey.PublicKey)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	v, err := auth.New(ctx, auth.Config{Issuer: issuer, Audience: audience, JWKSURL: srv.URL}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := v.Verify(sign(t, jwt.SigningMethodRS256, rsaKey, validClaims(), "rsa-1")); err != nil {
		t.Errorf("RS256 token: %v", err)
	}
	if _, err := v.Verify(sign(t, jwt.SigningMethodES256, ecKey, validClaims(), "ec-1")); err != nil {
		t.Errorf("ES256 token: %v", err)
	}

	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(sign(t, jwt.SigningMethodRS256, otherKey, validClaims(), "rsa-1")); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("token signed by an unknown key: %v", err)
	}
	if _, err := v.Verify(sign(t, jwt.SigningMethodRS256, rsaKey, validClaims(), "missing")); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("token with an unknown kid: %v", err)
	}
	// A JWKS verifier must never accept shared-secret tokens.
	if _, err := v.Verify(sign(t, jwt.SigningMethodHS256, secret, validClaims(), "rsa-1")); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("HS256 token accepted by the JWKS verifier: %v", err)
	}
}

func TestNewRequiresExactlyOneMethod(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	if _, err := auth.New(ctx, auth.Config{Issuer: issuer, Audience: audience}, log); err == nil {
		t.Error("expected an error without secret or JWKS URL")
	}
	if _, err := auth.New(ctx, auth.Config{HS256Secret: secret, JWKSURL: "https://idp.test/jwks"}, log); err == nil {
		t.Error("expected an error with both secret and JWKS URL")
	}
	if _, err := auth.New(ctx, hsConfig(), log); err != nil {
		t.Errorf("HS256 config: %v", err)
	}
}

func TestPrincipalContext(t *testing.T) {
	if _, ok := auth.PrincipalFrom(context.Background()); ok {
		t.Error("empty context must not carry a principal")
	}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Subject: "alice"})
	if p, ok := auth.PrincipalFrom(ctx); !ok || p.Subject != "alice" {
		t.Errorf("PrincipalFrom = %+v, %v", p, ok)
	}
}
