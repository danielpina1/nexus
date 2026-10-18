package main

// Node modules compiled into nexus-node: the daemon, edge verification,
// model access and runtimes. Adding a runtime = one blank import here.
import (
	_ "github.com/danielpina1/nexus/backend/internal/llm"
	_ "github.com/danielpina1/nexus/backend/internal/node"
	_ "github.com/danielpina1/nexus/backend/internal/runtime/anthrex"
	_ "github.com/danielpina1/nexus/backend/internal/runtime/browser"
	_ "github.com/danielpina1/nexus/backend/internal/runtime/claudecode"
	_ "github.com/danielpina1/nexus/backend/internal/runtime/codex"
	_ "github.com/danielpina1/nexus/backend/internal/runtime/echo"
	_ "github.com/danielpina1/nexus/backend/internal/runtime/openai"
	_ "github.com/danielpina1/nexus/backend/internal/runtime/shell"
	_ "github.com/danielpina1/nexus/backend/internal/security/nodetrust"
	_ "github.com/danielpina1/nexus/backend/internal/webtools"
)
