// Package schemas provides the canonical public JSON schemas.
package schemas

import _ "embed"

// PluginManifest is the EPP/1 plugin manifest JSON Schema.
//
//go:embed plugin-manifest.v1.json
var PluginManifest string

// PluginSources describes source queries, candidates and release hints.
//
//go:embed plugin-sources.v1.json
var PluginSources string

// PluginScreen is the plugin screen and surface document schema, generated
// by Vela (packages/protocol/schemas/plugin-screen.v1.json, from
// packages/protocol/screen.ts) and vendored verbatim; PluginScreenDigest is
// the SHA-256 Vela records beside its RENDERER_VERSION.
//
//go:embed plugin-screen.v1.json
var PluginScreen string

// PluginScreenDigest identifies the vendored copy of PluginScreen.
const PluginScreenDigest = "2f2337dac00e51333ea56ba64b71a9b9b085d4e405e8b18f8a4db77d2b0d81ee"
