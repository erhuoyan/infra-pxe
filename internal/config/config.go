package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Engine  EngineSection  `yaml:"engine"`
	Dnsmasq DnsmasqSection `yaml:"dnsmasq"`
	Data    DataSection    `yaml:"data"`
	Paths   PathsSection   `yaml:"paths"`

	// Webhook — optional external URL to forward events to (e.g. CMDB, controller)
	Webhook WebhookSection `yaml:"webhook"`

	// Resolved base directory (parent of config file)
	BaseDir string `yaml:"-"`
}

// WebhookSection configures optional event forwarding to an external system.
type WebhookSection struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
}

type EngineSection struct {
	Listen    string `yaml:"listen"` // IP to bind (default 0.0.0.0)
	Port      int    `yaml:"port"`
	Interface string `yaml:"interface"` // Deprecated: DHCP interface now stored in DB. Kept for yaml parse compat.
	Name      string `yaml:"name"`      // Instance name (for identification)
}

type DnsmasqSection struct {
	Binary     string `yaml:"binary"`
	ConfDir    string `yaml:"conf_dir"`
	PidFile    string `yaml:"pid_file"`
	LeaseTime  string `yaml:"lease_time"`
	DhcpScript string `yaml:"dhcp_script"`
}

type DataSection struct {
	Dir string `yaml:"dir"`
}

type PathsSection struct {
	BootDir      string `yaml:"boot_dir"`      // path to boot/ (tftp/http/iso)
	TemplatesDir string `yaml:"templates_dir"` // path to templates/ (ks.cfg.j2, scripts/)
}

func (c *Config) TasksDir() string {
	return filepath.Join(c.DataDir(), "tasks")
}

func (c *Config) ResultsDir() string {
	return filepath.Join(c.DataDir(), "results")
}

// DataDir returns the resolved data directory.
func (c *Config) DataDir() string {
	if c.Data.Dir != "" {
		if filepath.IsAbs(c.Data.Dir) {
			return c.Data.Dir
		}
		return filepath.Join(c.BaseDir, c.Data.Dir)
	}
	return filepath.Join(c.BaseDir, "data")
}

// LogDir returns the resolved log directory (inside data dir).
func (c *Config) LogDir() string {
	return filepath.Join(c.DataDir(), "logs")
}

func (c *Config) DnsmasqConfDir() string {
	if c.Dnsmasq.ConfDir != "" {
		if filepath.IsAbs(c.Dnsmasq.ConfDir) {
			return c.Dnsmasq.ConfDir
		}
		return filepath.Join(c.BaseDir, c.Dnsmasq.ConfDir)
	}
	return filepath.Join(c.BaseDir, "data", "dnsmasq")
}

// BootDir returns the resolved boot directory (contains tftp/, http/, iso/).
func (c *Config) BootDir() string {
	if c.Paths.BootDir != "" {
		if filepath.IsAbs(c.Paths.BootDir) {
			return c.Paths.BootDir
		}
		return filepath.Join(c.BaseDir, c.Paths.BootDir)
	}
	return filepath.Join(c.BaseDir, "boot")
}

// TemplatesDir returns the resolved templates directory.
func (c *Config) TemplatesDir() string {
	if c.Paths.TemplatesDir != "" {
		if filepath.IsAbs(c.Paths.TemplatesDir) {
			return c.Paths.TemplatesDir
		}
		return filepath.Join(c.BaseDir, c.Paths.TemplatesDir)
	}
	return filepath.Join(c.BaseDir, "templates")
}

// ListenAddr returns the resolved listen address.
func (c *Config) ListenAddr() string {
	if c.Engine.Listen != "" {
		return c.Engine.Listen
	}
	return "0.0.0.0"
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	// BaseDir = working directory (not config file dir).
	// systemd sets WorkingDirectory to the pxe directory,
	// so relative paths in config resolve from there.
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	cfg.BaseDir = cwd

	// Defaults
	if cfg.Engine.Port == 0 {
		cfg.Engine.Port = 9200
	}
	if cfg.Dnsmasq.Binary == "" {
		cfg.Dnsmasq.Binary = "dnsmasq"
	}
	if cfg.Dnsmasq.LeaseTime == "" {
		cfg.Dnsmasq.LeaseTime = "5m"
	}
	if cfg.Data.Dir == "" {
		cfg.Data.Dir = "data"
	}

	return &cfg, nil
}
