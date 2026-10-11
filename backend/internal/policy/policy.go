// Package policy provides the trust-level policy engine (kernel.PolicyEngine).
//
// Module "policy" (hub, default enabled). Provides kernel.SvcPolicy.
//
// Defaults follow docs/AGENTS.md §7. Where the table lists two levels
// ("auto / notify", "notify / approve") the stricter one is the default.
// Unknown/unspecified action classes require approval (fail safe).
//
// Config (nexus.yaml) may set any level, in either direction — it is the
// owner's global policy:
//
//	policy:
//	  levels:
//	    write_internal: auto
//	    device_control: notify
//
// An agent's policy map (agent note frontmatter) can only tighten: the
// effective level is max(global, agent override). Agent keys that are not
// action classes (e.g. "substitutions") are ignored here.
package policy

import (
	"context"
	"fmt"
	"strings"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

func init() { kernel.RegisterModule(func() kernel.Module { return &Module{} }) }

// Defaults is the built-in level per action class (docs/AGENTS.md §7).
var Defaults = map[nexusv1.ActionClass]nexusv1.TrustLevel{
	nexusv1.ActionClass_ACTION_CLASS_READ:           nexusv1.TrustLevel_TRUST_LEVEL_AUTO,
	nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL: nexusv1.TrustLevel_TRUST_LEVEL_NOTIFY,
	nexusv1.ActionClass_ACTION_CLASS_COMPUTE:        nexusv1.TrustLevel_TRUST_LEVEL_AUTO,
	nexusv1.ActionClass_ACTION_CLASS_WRITE_EXTERNAL: nexusv1.TrustLevel_TRUST_LEVEL_APPROVE,
	nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH:      nexusv1.TrustLevel_TRUST_LEVEL_APPROVE,
	nexusv1.ActionClass_ACTION_CLASS_SPEND:          nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC,
	nexusv1.ActionClass_ACTION_CLASS_DESTRUCTIVE:    nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC,
	nexusv1.ActionClass_ACTION_CLASS_DEVICE_CONTROL: nexusv1.TrustLevel_TRUST_LEVEL_APPROVE,
}

// unknownLevel applies to ACTION_CLASS_UNSPECIFIED and unknown classes.
const unknownLevel = nexusv1.TrustLevel_TRUST_LEVEL_APPROVE

// Config is the "policy" config section.
type Config struct {
	// Levels overrides defaults: action class name → level name.
	Levels map[string]string `yaml:"levels"`
}

// Engine implements kernel.PolicyEngine.
type Engine struct {
	levels map[nexusv1.ActionClass]nexusv1.TrustLevel
}

var _ kernel.PolicyEngine = (*Engine)(nil)

// New builds an engine from defaults plus cfg overrides.
func New(cfg Config) (*Engine, error) {
	e := &Engine{levels: map[nexusv1.ActionClass]nexusv1.TrustLevel{}}
	for c, l := range Defaults {
		e.levels[c] = l
	}
	for k, v := range cfg.Levels {
		c, ok := ParseActionClass(k)
		if !ok {
			return nil, fmt.Errorf("policy: unknown action class %q", k)
		}
		l, ok := ParseTrustLevel(v)
		if !ok {
			return nil, fmt.Errorf("policy: unknown trust level %q for %s", v, k)
		}
		e.levels[c] = l
	}
	return e, nil
}

// Level returns the trust level for class, tightened by the agent's policy.
func (e *Engine) Level(_ context.Context, agent *nexusv1.Agent, class nexusv1.ActionClass) (nexusv1.TrustLevel, error) {
	level, ok := e.levels[class]
	if !ok {
		level = unknownLevel
	}
	for k, override := range agent.GetPolicy() {
		if c, ok := ParseActionClass(k); ok && c == class && override > level && override <= nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC {
			level = override
		}
	}
	return level, nil
}

// ParseActionClass accepts "spend", "SPEND", "write-external" or "ACTION_CLASS_SPEND".
func ParseActionClass(s string) (nexusv1.ActionClass, bool) {
	n := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), "-", "_"))
	if !strings.HasPrefix(n, "ACTION_CLASS_") {
		n = "ACTION_CLASS_" + n
	}
	v, ok := nexusv1.ActionClass_value[n]
	if !ok || v == 0 {
		return 0, false
	}
	return nexusv1.ActionClass(v), true
}

// ParseTrustLevel accepts "approve", "APPROVE" or "TRUST_LEVEL_APPROVE".
func ParseTrustLevel(s string) (nexusv1.TrustLevel, bool) {
	n := strings.ToUpper(strings.TrimSpace(s))
	if !strings.HasPrefix(n, "TRUST_LEVEL_") {
		n = "TRUST_LEVEL_" + n
	}
	v, ok := nexusv1.TrustLevel_value[n]
	if !ok || v == 0 {
		return 0, false
	}
	return nexusv1.TrustLevel(v), true
}

// Module is the policy module.
type Module struct {
	kernel.BaseModule
	Engine *Engine
}

func (*Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           "policy",
		Description:    "Trust levels per action class (auto / notify / approve / biometric)",
		Side:           kernel.SideHub,
		DefaultEnabled: true,
	}
}

func (m *Module) Init(_ context.Context, h kernel.Host) error {
	var cfg Config
	if err := h.Config(&cfg); err != nil {
		return fmt.Errorf("policy: config: %w", err)
	}
	e, err := New(cfg)
	if err != nil {
		return err
	}
	m.Engine = e
	h.Provide(kernel.SvcPolicy, e)
	return nil
}
