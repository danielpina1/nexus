package main

// Node modules compiled into nexus-node. Integrators add runtimes here.
import (
	_ "github.com/danielpina1/nexus/backend/internal/node"
	_ "github.com/danielpina1/nexus/backend/internal/runtime/echo"
	_ "github.com/danielpina1/nexus/backend/internal/runtime/shell"
)
