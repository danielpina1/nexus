package anthrexrt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/anthrex"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Config is the module section "anthrex".
type Config struct {
	Socket string            `yaml:"socket"`
	Bin    string            `yaml:"bin"`
	Mode   string            `yaml:"mode"` // auto | socket | cli
	Env    map[string]string `yaml:"env"`
	// AllowedRepos: goals and new windows may only target these
	// repositories (or directories under them). Empty = refuse.
	AllowedRepos        []string `yaml:"allowed_repos"`
	DefaultDelivery     string   `yaml:"default_delivery"`
	DefaultOrchestrator string   `yaml:"default_orchestrator"`
	Design              string   `yaml:"design"`
	// PlanApproval: "approve" (default) asks the Nexus owner at Anthrex's
	// plan gate; "auto" starts goals with --yes (no plan gate).
	PlanApproval   string `yaml:"plan_approval"`
	TrustProject   bool   `yaml:"trust_project"`
	ReportMaxBytes int    `yaml:"report_max_bytes"`
	Tools          *bool  `yaml:"tools"`
}

func (c *Config) defaults() {
	if c.DefaultDelivery == "" {
		c.DefaultDelivery = "local"
	}
	if c.Design == "" {
		c.Design = "off"
	}
	if c.PlanApproval == "" {
		c.PlanApproval = "approve"
	}
	if c.ReportMaxBytes <= 0 {
		c.ReportMaxBytes = 64 << 10
	}
}

func (c Config) openOptions(h kernel.Host) anthrex.OpenOptions {
	o := anthrex.OpenOptions{Mode: c.Mode, Bin: c.Bin, Env: c.Env}
	o.Socket = c.Socket
	if h != nil {
		o.Logger = h.Logger().With("module", "anthrex")
	}
	return o
}

// ErrNoRepos is returned when anthrex.allowed_repos is empty.
var ErrNoRepos = errors.New("anthrex: no repositories allowed — set anthrex.allowed_repos in the node config (e.g. [~/src/myrepo]) to let Nexus start Anthrex goals or windows")

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func realAbs(p string) (string, error) {
	abs, err := filepath.Abs(expandHome(p))
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	return filepath.Clean(abs), nil
}

// AllowedRepo resolves dir and checks it lies in (or is) an allowed
// repository. Symlinks are resolved on both sides.
func (c Config) AllowedRepo(dir string) (string, error) {
	if len(c.AllowedRepos) == 0 {
		return "", ErrNoRepos
	}
	if strings.TrimSpace(dir) == "" {
		if len(c.AllowedRepos) == 1 {
			dir = c.AllowedRepos[0]
		} else {
			return "", fmt.Errorf("anthrex: no repository given (allowed: %s)", strings.Join(c.AllowedRepos, ", "))
		}
	}
	abs, err := realAbs(dir)
	if err != nil {
		return "", err
	}
	for _, root := range c.AllowedRepos {
		ra, err := realAbs(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(ra, abs)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			st, err := os.Stat(abs)
			if err != nil || !st.IsDir() {
				return "", fmt.Errorf("anthrex: %s is not a directory", abs)
			}
			return abs, nil
		}
	}
	return "", fmt.Errorf("anthrex: %s is not under anthrex.allowed_repos (%s)", abs, strings.Join(c.AllowedRepos, ", "))
}

// AgentSettings is the agent note's `anthrex:` block, forwarded in
// RunAssignment.context under "anthrex":
//
//	anthrex:
//	  repo: ~/src/nexus      # default repository (must be allowed)
//	  delivery: local        # local | pr
//	  design: off            # off | full
//	  orchestrator: claude   # claude | codex[:model]
//	  plan_approval: approve # approve | auto (only tightens: auto needs the node to allow it)
type AgentSettings struct {
	Repo         string
	Delivery     string
	Design       string
	Orchestrator string
	PlanApproval string
}

// SettingsFrom reads the "anthrex" block of the run context.
func SettingsFrom(a *nexusv1.RunAssignment) AgentSettings {
	var s AgentSettings
	v := a.GetContext().GetFields()["anthrex"].GetStructValue()
	if v == nil {
		return s
	}
	m := v.AsMap()
	get := func(k string) string {
		x, _ := m[k].(string)
		return strings.TrimSpace(x)
	}
	s.Repo, s.Delivery, s.Design = get("repo"), get("delivery"), get("design")
	s.Orchestrator, s.PlanApproval = get("orchestrator"), get("plan_approval")
	return s
}

// goalRequest builds the StartGoal request for an assignment.
func (c Config) goalRequest(a *nexusv1.RunAssignment) (anthrex.StartGoalRequest, error) {
	s := SettingsFrom(a)
	dir := a.GetContext().GetFields()["repo_dir"].GetStringValue()
	if dir == "" {
		dir = s.Repo
	}
	repo, err := c.AllowedRepo(dir)
	if err != nil {
		return anthrex.StartGoalRequest{}, err
	}
	goal := strings.TrimSpace(a.GetPrompt())
	if goal == "" {
		return anthrex.StartGoalRequest{}, errors.New("anthrex: the task has no goal (empty prompt)")
	}
	if n := len([]rune(goal)); n > goalMaxChars {
		return anthrex.StartGoalRequest{}, fmt.Errorf("anthrex: goal is %d characters (Anthrex accepts at most %d)", n, goalMaxChars)
	}
	g := anthrex.StartGoalRequest{
		Goal: goal, Dir: repo, TrustProject: c.TrustProject,
		Delivery: first(s.Delivery, c.DefaultDelivery), Design: first(s.Design, c.Design),
		Orchestrator: first(s.Orchestrator, c.DefaultOrchestrator),
	}
	// An agent can ask for a plan gate on a node set to auto, never the
	// reverse.
	g.Yes = c.PlanApproval == "auto" && s.PlanApproval != "approve"
	if _, err := anthrex.ReqStartGoal(g); err != nil {
		return anthrex.StartGoalRequest{}, err
	}
	return g, nil
}

// goalMaxChars is proto::GOAL_MAX_CHARS.
const goalMaxChars = 16384

func first(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
