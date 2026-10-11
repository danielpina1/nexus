// Package hubserver builds and runs the hub's HTTP server: Connect services
// mounted by modules, auth, CORS, health checks and the web dashboard.
package hubserver

import (
	"github.com/danielpina1/nexus/backend/internal/host"
)

// DefaultListen is the hub API address.
const DefaultListen = ":7700"

// DefaultCORSOrigins are allowed when cors.allowed_origins is not configured
// (the Vite dev server).
var DefaultCORSOrigins = []string{"http://localhost:5173", "http://127.0.0.1:5173"}

// Config is the "server" section of nexus.yaml.
//
//	server:
//	  listen: ":7700"
//	  web_dir: ../web/dist
//	  cors:
//	    allowed_origins: ["http://localhost:5173"]   # "*" allows any origin
//	  tls:
//	    cert: /path/cert.pem
//	    key: /path/key.pem
type Config struct {
	Listen string `yaml:"listen"`
	// WebDir holds the built dashboard (index.html + assets). Empty disables it.
	WebDir string `yaml:"web_dir"`
	CORS   struct {
		// AllowedOrigins; nil means DefaultCORSOrigins, an empty list disables CORS.
		AllowedOrigins []string `yaml:"allowed_origins"`
	} `yaml:"cors"`
	TLS struct {
		Cert string `yaml:"cert"`
		Key  string `yaml:"key"`
	} `yaml:"tls"`
}

// LoadConfig reads the "server" section of the process config and applies defaults.
func LoadConfig(c *host.Config) (Config, error) {
	var cfg Config
	if c != nil {
		if err := c.Section("server", &cfg); err != nil {
			return cfg, err
		}
	}
	cfg.applyDefaults()
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if c.CORS.AllowedOrigins == nil {
		c.CORS.AllowedOrigins = DefaultCORSOrigins
	}
}
