// Package protocol provides the versioned protocol contract files.
package protocol

import (
	"embed"
	"io/fs"
)

//go:embed schemas
var schemaBundle embed.FS

// SchemaFiles returns the schemas bundled into the controller binary. Runtime
// validation therefore never needs to fetch a schema from the network.
func SchemaFiles() fs.FS {
	return schemaBundle
}
