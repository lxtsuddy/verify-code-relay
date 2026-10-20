package config

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Empty returns a config with no bots, targets or mailboxes; it is valid once defaulted.
func Empty() *Config {
	return &Config{Bots: map[string]Bot{}, Targets: map[string]Target{}, Mailboxes: map[string]*Mailbox{}}
}

// Decode parses YAML exactly as stored: ${ENV} is not expanded and no defaults are applied.
func Decode(data []byte) (*Config, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("解析 YAML：%w", err)
	}
	c, err := decodeNode(&root)
	if err != nil {
		return nil, err
	}
	c.fillMaps()
	return c, nil
}

// Encode writes the config as YAML, leaving out every field that was not set.
func Encode(c *Config) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Clone returns a deep copy.
func (c *Config) Clone() *Config {
	data, err := Encode(c)
	if err != nil {
		panic(fmt.Sprintf("config: encode for clone: %v", err)) // every Config value is encodable
	}
	out, err := Decode(data)
	if err != nil {
		panic(fmt.Sprintf("config: decode for clone: %v", err))
	}
	return out
}

// Defaulted returns a copy with defaults applied; c is not modified.
func (c *Config) Defaulted() *Config {
	d := c.Clone()
	d.applyDefaults()
	return d
}

// LoadRaw reads a YAML file, expands ${ENV} and decodes it without applying defaults.
func LoadRaw(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("讀取設定檔：%w", err)
	}
	return LoadRawBytes(data, os.LookupEnv)
}

// LoadRawBytes is LoadRaw for data already in memory.
func LoadRawBytes(data []byte, lookup func(string) (string, bool)) (*Config, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("解析 YAML：%w", err)
	}
	if err := expandNode(&root, "", lookup); err != nil {
		return nil, err
	}
	c, err := decodeNode(&root)
	if err != nil {
		return nil, err
	}
	c.fillMaps()
	return c, nil
}

// fillMaps initialises nil maps and sets Mailbox.Name, so callers can add entries directly.
func (c *Config) fillMaps() {
	if c.Bots == nil {
		c.Bots = map[string]Bot{}
	}
	if c.Targets == nil {
		c.Targets = map[string]Target{}
	}
	if c.Mailboxes == nil {
		c.Mailboxes = map[string]*Mailbox{}
	}
	for name, m := range c.Mailboxes {
		if m == nil {
			m = &Mailbox{}
			c.Mailboxes[name] = m
		}
		m.Name = name
	}
}
