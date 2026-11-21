// Package gen holds code and data generated from proto/ by `make gen`.
package gen

import _ "embed"

// Descriptors is a FileDescriptorSet of every proto under proto/, with
// source info, so the comments on messages, fields and enum values are
// available at run time.
//
//go:embed descriptors.binpb
var Descriptors []byte
