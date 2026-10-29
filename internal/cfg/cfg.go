// Package cfg holds wxdl's own configuration and on-disk layout.
package cfg

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const (
	DefaultProxyPort = 2023
	DefaultAPIPort   = 2022
	DefaultCAName    = "wxdl-local-ca"
)

// Config is persisted at ~/.wxdl/config.json.
type Config struct {
	DownloadDir string `json:"download_dir"`
	CAName      string `json:"ca_name"`
	ProxyPort   int    `json:"proxy_port"`
	APIPort     int    `json:"api_port"`
	// UpstreamTag pins the upstream release; empty means "use latest".
	UpstreamTag string `json:"upstream_tag"`
	GitHubToken string `json:"github_token"`
	// CertFile and CertKey point at the machine-specific root CA once it has
	// been generated, so later runs never mint a second one.
	CertFile string `json:"cert_file"`
	CertKey  string `json:"cert_key"`
}

// Paths are wxdl's working directories under ~/.wxdl.
type Paths struct {
	Home    string
	Bin     string
	Cache   string
	Work    string
	Run     string
	CertDir string
}

func Home() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".wxdl"), nil
}

func (p Paths) Ensure() error {
	// CertDir holds the private key for the trusted root certificate, so it is
	// the one directory that must not be world-readable.
	dirs := []struct {
		path string
		mode os.FileMode
	}{
		{p.Home, 0o755},
		{p.Bin, 0o755},
		{p.Cache, 0o755},
		{p.Work, 0o755},
		{p.Run, 0o755},
		{p.CertDir, 0o700},
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d.path, d.mode); err != nil {
			return err
		}
		// MkdirAll leaves an already-existing directory untouched.
		if d.path == p.CertDir {
			_ = os.Chmod(d.path, 0o700)
		}
	}
	return nil
}

// Layout resolves every directory wxdl owns.
func Layout() (Paths, error) {
	home, err := Home()
	if err != nil {
		return Paths{}, err
	}
	return Paths{
		Home:    home,
		Bin:     filepath.Join(home, "bin"),
		Cache:   filepath.Join(home, "cache"),
		Work:    filepath.Join(home, "work"),
		Run:     filepath.Join(home, "run"),
		CertDir: filepath.Join(home, "certs"),
	}, nil
}

func configPath() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "config.json"), nil
}

// Load reads the config, filling defaults for missing values.
func Load() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	c := &Config{}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(raw, c); err != nil {
			return nil, err
		}
	}
	c.applyDefaults()
	return c, nil
}

func (c *Config) applyDefaults() {
	if c.CAName == "" {
		c.CAName = DefaultCAName
	}
	if c.ProxyPort == 0 {
		c.ProxyPort = DefaultProxyPort
	}
	if c.APIPort == 0 {
		c.APIPort = DefaultAPIPort
	}
	if c.DownloadDir == "" {
		if h, err := os.UserHomeDir(); err == nil {
			c.DownloadDir = filepath.Join(h, "Downloads", "wxdl")
		} else {
			c.DownloadDir = filepath.Join("Downloads", "wxdl")
		}
	}
}

// Save writes the config, creating ~/.wxdl when needed.
func (c *Config) Save() error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}
