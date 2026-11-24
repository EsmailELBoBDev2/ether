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
const PluginScreenDigest = "606dd01264f0b5c3d639bd8ac990e8c181e9e5db0b671881ae03f8eb5c5423fe"
