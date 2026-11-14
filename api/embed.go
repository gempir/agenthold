// Package api embeds the OpenAPI document, the source of truth for the HTTP API.
package api

import _ "embed"

// OpenAPI is api/openapi.yaml.
//
//go:embed openapi.yaml
var OpenAPI []byte
