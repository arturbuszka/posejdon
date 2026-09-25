// Package web zawiera PWA wbudowaną w binarkę.
package web

import "embed"

//go:embed *.html *.js *.css *.webmanifest icons
var Files embed.FS
