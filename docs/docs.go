// Package docs embeds the protocol specification so the server can serve it.
package docs

import _ "embed"

// Spec is the protocol specification (SPEC.md).
//
//go:embed SPEC.md
var Spec []byte
