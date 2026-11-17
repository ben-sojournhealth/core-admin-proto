// Package seeddata embeds the protojson seed fixtures so binaries and
// tests can load them without depending on the working directory.
package seeddata

import "embed"

//go:embed */*.json
var FS embed.FS
