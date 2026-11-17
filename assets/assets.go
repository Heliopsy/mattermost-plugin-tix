// SPDX-License-Identifier: AGPL-3.0-only

// Package assets embeds the bundle files the server reads at run time, so they are checked at
// build time and tests see the same files the plugin ships.
package assets

import "embed"

// I18n holds the server's message catalogs, i18n/<language>.json.
//
//go:embed i18n/*.json
var I18n embed.FS
