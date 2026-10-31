// Package auth verifies JWT access tokens and carries the authenticated
// principal through request contexts.
package auth

import (
	"context"
	"slices"
)

// Scopes understood by the API.
const (
	ScopeRead   = "devices:read"
	ScopeWrite  = "devices:write"
	ScopeDelete = "devices:delete"
)

// Principal is the authenticated caller.
type Principal struct {
	// Subject is the "sub" claim: who the token was issued to.
	Subject string
	Scopes  []string
}

// HasScope reports whether the principal was granted scope.
func (p Principal) HasScope(scope string) bool {
	return slices.Contains(p.Scopes, scope)
}

type principalKey struct{}

// WithPrincipal returns a copy of ctx carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal stored in ctx, if any.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
