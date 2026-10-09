// Package config loads and validates config.yml.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

type Config struct {
	DistSpecVersion string  `json:"distSpecVersion"`
	Log             Log     `json:"log"`
	HTTP            HTTP    `json:"http"`
	Proxy           Proxy   `json:"proxy"`
	Storage         Storage `json:"storage"`
}

type Log struct {
	Level string `json:"level"`
}

type HTTP struct {
	Address string `json:"address"`
	Port    string `json:"port"`
	TLS     *TLS   `json:"tls"`
}

type TLS struct {
	Cert string `json:"cert"`
	Key  string `json:"key"`
}

type Proxy struct {
	Default    string              `json:"default"`
	Registries map[string]Registry `json:"registries"`
	// NotFoundTTL is how long a ref upstream does not have answers not found
	// without another pull; 0 disables it.
	NotFoundTTL Duration `json:"notFoundTTL"`
}

// Duration is a time.Duration written as a Go duration string, such as "10s".
type Duration time.Duration

func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("duration %s is not a string such as \"10s\"", data)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

type Registry struct{}

type Storage struct {
	Containerd Containerd `json:"containerd"`
}

type Containerd struct {
	Address string `json:"address"`
}

// Load reads a JSON or YAML config file, applies defaults and validates it.
// Unknown keys are an error, so a misspelled or unsupported option fails loudly.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	js, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	// Set before decoding, as 0 is a valid value that setDefaults cannot tell from unset.
	c := Config{Proxy: Proxy{NotFoundTTL: Duration(10 * time.Second)}}
	dec := json.NewDecoder(bytes.NewReader(js))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.setDefaults()
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) setDefaults() {
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.HTTP.Address == "" {
		c.HTTP.Address = "0.0.0.0"
	}
	if c.HTTP.Port == "" {
		c.HTTP.Port = "5000"
	}
	if c.Storage.Containerd.Address == "" {
		c.Storage.Containerd.Address = "/run/containerd/containerd.sock"
	}
}

func (c *Config) validate() error {
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log.level: %q is not one of debug, info, warn, error", c.Log.Level)
	}
	if t := c.HTTP.TLS; t != nil && (t.Cert == "" || t.Key == "") {
		return errors.New("http.tls: cert and key are both required")
	}
	for host := range c.Proxy.Registries {
		if host == "" || strings.Contains(host, "/") {
			return fmt.Errorf("proxy.registries: key %q is not a host name", host)
		}
	}
	if c.Proxy.NotFoundTTL < 0 {
		return fmt.Errorf("proxy.notFoundTTL: %v is negative", time.Duration(c.Proxy.NotFoundTTL))
	}
	if d := c.Proxy.Default; d != "" {
		if _, ok := c.Proxy.Registries[d]; !ok {
			return fmt.Errorf("proxy.default: %q is not a key of proxy.registries", d)
		}
	}
	return nil
}
