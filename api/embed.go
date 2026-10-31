// Package api embeds the OpenAPI contract so the binary can serve it.
package api

import _ "embed"

// OpenAPI is the OpenAPI 3.1 document describing the HTTP API.
//
//go:embed openapi.yaml
var OpenAPI []byte
