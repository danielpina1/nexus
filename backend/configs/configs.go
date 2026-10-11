// Package configs embeds example configuration files shipped with the binaries.
package configs

import _ "embed"

// NexusExample is nexus.example.yaml, written by `nexusd init`.
//
//go:embed nexus.example.yaml
var NexusExample []byte
