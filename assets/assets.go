// Package assets embeds KyanDB's canonical brand SVG assets.
package assets

import _ "embed"

// LogoSVG is the tight-cropped KyanDB mark for light and dark UI surfaces.
//
//go:embed logo.svg
var LogoSVG []byte
