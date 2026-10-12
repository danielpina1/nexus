package notify

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/danielpina1/nexus/backend/internal/notify/apns"
)

// Config is the "notify" section of nexus.yaml.
//
//	notify:
//	  apns:
//	    team_id: "ABCDE12345"
//	    key_id: "XYZ9876543"
//	    key_file: "/etc/nexus/AuthKey_XYZ9876543.p8"  # or key_env: NEXUS_APNS_KEY (PEM contents)
//	    bundle_id: "com.example.nexus"
//	    environment: sandbox            # sandbox | production (default production)
//	    base_url: ""                    # override host (tests / proxies)
//	  encrypt: true                     # false = plaintext alerts (DEV ONLY)
//	  live_activity:
//	    enabled: true
//	    min_interval: 10s               # coalescing window for progress updates
//	    dismiss_after: 1h               # dismissal-date after "end"
//	    stale_after: 15m                # stale-date on every update
//	    max_duration: 8h                # end Run activities older than this
//	    show_details: false             # titles/details on the Lock Screen (privacy)
//	    pulse: true                     # aggregate Pulse activity
//	    auto_follow_agents: []          # agent ids whose root tasks always get a Run activity
//	  hub_name: "Nexus"                 # PulseActivityAttributes.hubName
//	  node_offline_after: 2m            # debounce before "node offline" alerts
//	  quiet_hours: {start: "22:00", end: "07:00", timezone: "Europe/Lisbon"}
//
// Without apns.team_id / key_id / key the module runs in dry-run mode:
// everything is composed (and sealed) but only logged.
type Config struct {
	APNS             APNSConfig         `yaml:"apns"`
	Encrypt          *bool              `yaml:"encrypt"`
	LiveActivity     LiveActivityConfig `yaml:"live_activity"`
	HubName          string             `yaml:"hub_name"`
	NodeOfflineAfter time.Duration      `yaml:"node_offline_after"`
	QuietHours       *QuietHours        `yaml:"quiet_hours"`
}

// APNSConfig holds the provider credentials.
type APNSConfig struct {
	TeamID      string `yaml:"team_id"`
	KeyID       string `yaml:"key_id"`
	KeyFile     string `yaml:"key_file"`
	KeyEnv      string `yaml:"key_env"`
	BundleID    string `yaml:"bundle_id"`
	Environment string `yaml:"environment"`
	BaseURL     string `yaml:"base_url"`
}

// LiveActivityConfig tunes the Live Activity manager.
type LiveActivityConfig struct {
	Enabled          *bool         `yaml:"enabled"`
	MinInterval      time.Duration `yaml:"min_interval"`
	DismissAfter     time.Duration `yaml:"dismiss_after"`
	StaleAfter       time.Duration `yaml:"stale_after"`
	MaxDuration      time.Duration `yaml:"max_duration"`
	ShowDetails      bool          `yaml:"show_details"`
	Pulse            *bool         `yaml:"pulse"`
	AutoFollowAgents []string      `yaml:"auto_follow_agents"`
}

// QuietHours downgrades non-time-sensitive notifications to passive (no
// sound, no banner) between Start and End (local "HH:MM", may wrap midnight).
type QuietHours struct {
	Start    string `yaml:"start"`
	End      string `yaml:"end"`
	Timezone string `yaml:"timezone"`

	start, end int // minutes since midnight
	loc        *time.Location
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func (c *Config) defaults() error {
	la := &c.LiveActivity
	if la.MinInterval <= 0 {
		la.MinInterval = 10 * time.Second
	}
	if la.DismissAfter <= 0 {
		la.DismissAfter = time.Hour
	}
	if la.StaleAfter <= 0 {
		la.StaleAfter = 15 * time.Minute
	}
	if la.MaxDuration <= 0 {
		la.MaxDuration = 8 * time.Hour
	}
	if c.HubName == "" {
		c.HubName = "Nexus"
	}
	if c.NodeOfflineAfter <= 0 {
		c.NodeOfflineAfter = 2 * time.Minute
	}
	if c.QuietHours != nil {
		if err := c.QuietHours.parse(); err != nil {
			return err
		}
	}
	return nil
}

// encrypt reports whether notification content is sealed (default true).
func (c *Config) encrypt() bool { return boolOr(c.Encrypt, true) }

// credentialsConfigured reports whether real APNs delivery is possible.
func (a *APNSConfig) credentialsConfigured() bool {
	return a.TeamID != "" && a.KeyID != "" && (a.KeyFile != "" || a.KeyEnv != "")
}

// loadKey reads the .p8 key from key_file or the key_env variable (which may
// hold PEM contents or a path).
func (a *APNSConfig) loadKey() ([]byte, error) {
	if a.KeyFile != "" {
		return os.ReadFile(a.KeyFile)
	}
	v := os.Getenv(a.KeyEnv)
	if v == "" {
		return nil, fmt.Errorf("notify: env %s is empty", a.KeyEnv)
	}
	if strings.Contains(v, "-----BEGIN") {
		return []byte(v), nil
	}
	return os.ReadFile(v)
}

func (a *APNSConfig) tokenSource() (*apns.TokenSource, error) {
	b, err := a.loadKey()
	if err != nil {
		return nil, err
	}
	k, err := apns.ParsePrivateKey(b)
	if err != nil {
		return nil, err
	}
	return &apns.TokenSource{TeamID: a.TeamID, KeyID: a.KeyID, Key: k}, nil
}

func parseHM(s string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("notify: quiet_hours: bad time %q (want HH:MM)", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

func (q *QuietHours) parse() error {
	if q.Start == "" || q.End == "" {
		return errors.New("notify: quiet_hours needs start and end")
	}
	var err error
	if q.start, err = parseHM(q.Start); err != nil {
		return err
	}
	if q.end, err = parseHM(q.End); err != nil {
		return err
	}
	q.loc = time.Local
	if q.Timezone != "" {
		if q.loc, err = time.LoadLocation(q.Timezone); err != nil {
			return fmt.Errorf("notify: quiet_hours timezone: %w", err)
		}
	}
	return nil
}

// Active reports whether t falls inside the quiet window.
func (q *QuietHours) Active(t time.Time) bool {
	if q == nil || q.loc == nil || q.start == q.end {
		return false
	}
	lt := t.In(q.loc)
	m := lt.Hour()*60 + lt.Minute()
	if q.start < q.end {
		return m >= q.start && m < q.end
	}
	return m >= q.start || m < q.end
}
