package main

// Hub modules compiled into nexusd. Each registers itself in init();
// configuration decides which ones run (`nexusd modules` lists them).
// Adding a capability to the hub = one blank import here.
import (
	_ "github.com/danielpina1/nexus/backend/internal/approvals"
	_ "github.com/danielpina1/nexus/backend/internal/command"
	_ "github.com/danielpina1/nexus/backend/internal/events"
	_ "github.com/danielpina1/nexus/backend/internal/llm"
	_ "github.com/danielpina1/nexus/backend/internal/nodelink"
	_ "github.com/danielpina1/nexus/backend/internal/notify"
	_ "github.com/danielpina1/nexus/backend/internal/policy"
	_ "github.com/danielpina1/nexus/backend/internal/router"
	_ "github.com/danielpina1/nexus/backend/internal/security"
	_ "github.com/danielpina1/nexus/backend/internal/shops"
	_ "github.com/danielpina1/nexus/backend/internal/store"
	_ "github.com/danielpina1/nexus/backend/internal/tasks"
	_ "github.com/danielpina1/nexus/backend/internal/triggers"
	_ "github.com/danielpina1/nexus/backend/internal/vault"
	_ "github.com/danielpina1/nexus/backend/internal/webtools"
	_ "github.com/danielpina1/nexus/backend/internal/workflow"
)
