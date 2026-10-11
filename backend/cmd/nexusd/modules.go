package main

// Hub modules compiled into nexusd. Each registers itself in init();
// configuration decides which ones run (`nexusd modules` lists them).
import (
	_ "github.com/danielpina1/nexus/backend/internal/events"
	_ "github.com/danielpina1/nexus/backend/internal/store"
)
