package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// ErrInvalidToken wraps every reason a token is rejected. The reason is
// meant for logs only; clients get a generic answer.
var ErrInvalidToken = errors.New("invalid access token")

// Config selects how tokens are verified. Exactly one of HS256Secret and
// JWKSURL must be set.
type Config struct {
	Issuer   string
	Audience string
	// HS256Secret enables shared-secret verification, intended for local
	// development and tests.
	HS256Secret []byte
	// JWKSURL enables asymmetric verification (RS256, ES256, PS256, EdDSA)
	// with keys fetched from an identity provider.
	JWKSURL string
	// JWKSRefresh is how often the key set is fetched again.
	JWKSRefresh time.Duration
	// ClockSkew tolerates small clock differences on exp, nbf and iat.
	ClockSkew time.Duration
}

// Claims are the JWT claims the API reads.
type Claims struct {
	// Scope is the space-separated OAuth 2.0 scope claim (RFC 8693).
	Scope string `json:"scope,omitempty"`
	// Scp is the array form used by some identity providers.
	Scp []string `json:"scp,omitempty"`
	jwt.RegisteredClaims
}

// Verifier validates access tokens.
type Verifier struct {
	parser  *jwt.Parser
	keyfunc jwt.Keyfunc
}

// New returns a verifier for cfg. With a JWKS URL it starts a background
// refresh of the key set that stops when ctx is done.
func New(ctx context.Context, cfg Config, logger *slog.Logger) (*Verifier, error) {
	switch {
	case len(cfg.HS256Secret) > 0 && cfg.JWKSURL == "":
		return NewHS256Verifier(cfg), nil
	case cfg.JWKSURL != "" && len(cfg.HS256Secret) == 0:
		return NewJWKSVerifier(ctx, cfg, logger)
	default:
		return nil, errors.New("auth: configure exactly one of an HS256 secret or a JWKS URL")
	}
}

// NewHS256Verifier verifies tokens signed with the shared secret in cfg.
func NewHS256Verifier(cfg Config) *Verifier {
	secret := cfg.HS256Secret
	return &Verifier{
		parser: newParser(cfg, jwt.SigningMethodHS256.Alg()),
		keyfunc: func(*jwt.Token) (any, error) {
			return secret, nil
		},
	}
}

// NewJWKSVerifier verifies tokens with the public keys published at
// cfg.JWKSURL. Keys are refreshed periodically and when a token names an
// unknown key id (rate limited by the library).
func NewJWKSVerifier(ctx context.Context, cfg Config, logger *slog.Logger) (*Verifier, error) {
	refresh := cfg.JWKSRefresh
	if refresh <= 0 {
		refresh = 15 * time.Minute
	}
	kf, err := keyfunc.NewDefaultOverrideCtx(ctx, []string{cfg.JWKSURL}, keyfunc.Override{
		HTTPTimeout:     5 * time.Second,
		RefreshInterval: refresh,
		RefreshErrorHandlerFunc: func(u string) func(context.Context, error) {
			return func(ctx context.Context, err error) {
				logger.ErrorContext(ctx, "failed to refresh JWKS", slog.String("url", u), slog.Any("error", err))
			}
		},
	})
	if err != nil {
		return nil, fmt.Errorf("auth: load JWKS: %w", err)
	}
	return &Verifier{
		parser:  newParser(cfg, "RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EdDSA"),
		keyfunc: kf.Keyfunc,
	}, nil
}

// newParser pins the accepted algorithms, which blocks "none" and
// algorithm-confusion attacks, and requires issuer, audience and expiry.
func newParser(cfg Config, algs ...string) *jwt.Parser {
	return jwt.NewParser(
		jwt.WithValidMethods(algs),
		jwt.WithIssuer(cfg.Issuer),
		jwt.WithAudience(cfg.Audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(cfg.ClockSkew),
	)
}

// Verify checks the signature and claims of raw and returns its principal.
func (v *Verifier) Verify(raw string) (Principal, error) {
	var claims Claims
	if _, err := v.parser.ParseWithClaims(raw, &claims, v.keyfunc); err != nil {
		return Principal{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if claims.Subject == "" {
		return Principal{}, fmt.Errorf("%w: missing subject", ErrInvalidToken)
	}

	scopes := strings.Fields(claims.Scope)
	scopes = append(scopes, claims.Scp...)
	return Principal{Subject: claims.Subject, Scopes: scopes}, nil
}

// SignHS256 issues a token signed with secret. It exists for local
// development (cmd/tokengen) and tests; production tokens come from an
// identity provider.
func SignHS256(secret []byte, issuer, audience, subject string, scopes []string, ttl time.Duration, now time.Time) (string, error) {
	claims := Claims{
		Scope: strings.Join(scopes, " "),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			Subject:   subject,
			Audience:  jwt.ClaimStrings{audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}
