package main

import (
	"testing"
)

func TestRunRequiresEnvironment(t *testing.T) {
	t.Setenv("AUTH_HS256_SECRET", "")
	t.Setenv("AUTH_ISSUER", "")
	t.Setenv("AUTH_AUDIENCE", "")
	if err := run(nil); err == nil {
		t.Error("expected an error without the auth variables")
	}
}

func TestRunRejectsInvalidFlags(t *testing.T) {
	t.Setenv("AUTH_HS256_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("AUTH_ISSUER", "issuer")
	t.Setenv("AUTH_AUDIENCE", "audience")
	if err := run([]string{"-ttl", "-1h"}); err == nil {
		t.Error("expected an error for a negative ttl")
	}
	if err := run([]string{"-unknown"}); err == nil {
		t.Error("expected an error for an unknown flag")
	}
	if err := run([]string{"-sub", "alice", "-scope", "devices:read"}); err != nil {
		t.Errorf("valid run: %v", err)
	}
}
