// Command tokengen prints an HS256 access token for local development.
//
// It reads AUTH_HS256_SECRET, AUTH_ISSUER and AUTH_AUDIENCE from the
// environment, the same variables the API uses, so the token is accepted by
// a local instance. Production deployments verify tokens from an identity
// provider through AUTH_JWKS_URL and have no use for this tool.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/erikmatioli/devices-api/internal/auth"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tokengen:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("tokengen", flag.ContinueOnError)
	subject := fs.String("sub", "local-developer", "subject (sub claim)")
	scope := fs.String("scope", strings.Join([]string{auth.ScopeRead, auth.ScopeWrite, auth.ScopeDelete}, " "),
		"space-separated scopes")
	ttl := fs.Duration("ttl", time.Hour, "token lifetime")
	if err := fs.Parse(args); err != nil {
		return err
	}

	secret := os.Getenv("AUTH_HS256_SECRET")
	issuer := os.Getenv("AUTH_ISSUER")
	audience := os.Getenv("AUTH_AUDIENCE")
	if secret == "" || issuer == "" || audience == "" {
		return errors.New("AUTH_HS256_SECRET, AUTH_ISSUER and AUTH_AUDIENCE must be set")
	}
	if *ttl <= 0 {
		return errors.New("-ttl must be positive")
	}

	token, err := auth.SignHS256([]byte(secret), issuer, audience, *subject, strings.Fields(*scope), *ttl, time.Now())
	if err != nil {
		return err
	}
	fmt.Println(token)
	return nil
}
