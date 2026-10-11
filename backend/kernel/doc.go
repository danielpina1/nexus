// Package kernel is the Nexus plug-in SDK.
//
// Everything in Nexus is a module: the API, the task engine, the vault, the
// node link, integrations, routers, notifiers. Modules register themselves in
// init() (like database/sql drivers), are enabled from configuration, and talk
// to each other only through the Host: shared registries (tools, runtimes,
// triggers), the event bus, and named services resolved by interface.
//
// Adding a capability to Nexus therefore means writing one package that
// implements Module and blank-importing it from cmd/nexusd or cmd/nexus-node.
//
// Two process kinds load modules:
//   - the Hub (nexusd): API, tasks, policy, vault, notifications, triggers…
//   - the Node (nexus-node): runtimes and local tools.
package kernel
