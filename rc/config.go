package rc

import (
	"io/ioutil"
	"os"
	"runtime"
	"strings"

	fmt "github.com/jhunt/go-ansi"
	"gopkg.in/yaml.v2"
)

type Config struct {
	Version int               `yaml:"version"`
	Current string            `yaml:"current"`
	Vaults  map[string]*Vault `yaml:"vaults"`
}

type Vault struct {
	URL        string `yaml:"url"`
	Token      string `yaml:"token"`
	SkipVerify bool   `yaml:"skip_verify"`
}

type oldConfig struct {
	Current    string                 `yaml:"Current"`
	Targets    map[string]interface{} `yaml:"Targets"`
	Aliases    map[string]string      `yaml:"Aliases"`
	SkipVerify map[string]bool        `yaml:"SkipVerify"`
}

func userHomeDir() string {
	if runtime.GOOS == "windows" {
		home := os.Getenv("USERPROFILE")
		if home == "" {
			home = os.Getenv("HOMEDRIVE") + os.Getenv("HOMEPATH")
		}
		return home
	}
	return os.Getenv("HOME")
}

func saferc() string {
	return fmt.Sprintf("%s/.saferc", userHomeDir())
}

func svtoken() string {
	return fmt.Sprintf("%s/.svtoken", userHomeDir())
}

func (legacy *oldConfig) convert() Config {
	c := Config{
		Version: 1,
		Current: legacy.Current,
		Vaults:  make(map[string]*Vault),
	}

	for alias, url := range legacy.Aliases {
		v := &Vault{
			URL: url,
		}
		if skip, ok := legacy.SkipVerify[url]; ok {
			v.SkipVerify = skip
		}
		if token, ok := legacy.Targets[url]; ok && token != nil {
			v.Token = token.(string)
		}
		c.Vaults[alias] = v
	}

	return c
}

func (c *Config) credentials() (string, string, bool, error) {
	if c.Current == "" {
		return "", "", false, nil
	}

	v, ok, _ := c.Find(c.Current)
	if !ok {
		return "", "", false, fmt.Errorf("Current target vault '%s' not found in ~/.saferc", c.Current)
	}

	return v.URL, v.Token, v.SkipVerify, nil
}

func Read() Config {
	var c Config

	b, err := ioutil.ReadFile(saferc())
	if err != nil {
		return Config{Version: 1}
	}

	if err = yaml.Unmarshal(b, &c); err != nil {
		return Config{Version: 1}
	}
	if c.Version == 0 {
		var legacy oldConfig
		if err = yaml.Unmarshal(b, &legacy); err != nil {
			fmt.Fprintf(os.Stderr, "@R{!!! %s}\n", err)
			os.Exit(1)
		}
		c = legacy.convert()
	}

	return c
}

func Apply(use string) Config {
	c := Read()

	if err := c.Apply(use); err != nil {
		fmt.Fprintf(os.Stderr, "@R{!!! %s}\n", err)
		os.Exit(1)
	}
	return c
}

func (c *Config) Write() error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}

	err = ioutil.WriteFile(saferc(), b, 0600)
	if err != nil {
		return err
	}

	v, err := c.Vault("")
	if err != nil {
		return err
	}
	if v == nil {
		os.Remove(svtoken())
		return nil
	}

	sv := struct {
		Vault      string `yaml:"vault"` /* this is different than Vault.URL */
		Token      string `yaml:"token"`
		SkipVerify bool   `yaml:"skip_verify"`
	}{v.URL, v.Token, v.SkipVerify}
	b, err = yaml.Marshal(sv)
	if err != nil {
		return err
	}

	return ioutil.WriteFile(svtoken(), b, 0600)
}

func (c *Config) Apply(use string) error {
	v, err := c.Vault(use)
	if err != nil {
		fmt.Fprintf(os.Stderr, "@R{!!! %s}\n", err)
		os.Exit(1)
	}

	if v != nil {
		os.Setenv("VAULT_ADDR", v.URL)
		os.Setenv("VAULT_TOKEN", v.Token)
		if v.SkipVerify {
			os.Setenv("VAULT_SKIP_VERIFY", "1")
		}
	} else {
		if os.Getenv("VAULT_TOKEN") == "" {
			tokenFile := fmt.Sprintf("%s/.vault-token", os.Getenv("HOME"))
			b, err := ioutil.ReadFile(tokenFile)
			if err == nil {
				os.Setenv("VAULT_TOKEN", strings.TrimSpace(string(b)))
			}
		}
	}
	return nil
}

func (c *Config) SetCurrent(alias string, reskip bool) error {
	v, ok, err := c.Find(alias)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("Unknown target '%s'", alias)
	}
	c.Current = alias
	if reskip {
		v.SkipVerify = true
	}
	return nil
}

func (c *Config) SetTarget(alias, url string, skipverify bool) error {
	if c.Vaults == nil {
		c.Vaults = make(map[string]*Vault)
	}

	c.Current = alias
	c.Vaults[alias] = &Vault{
		URL:        url,
		SkipVerify: skipverify,
	}

	return nil
}

func (c *Config) SetToken(token string) error {
	if c.Current == "" {
		return fmt.Errorf("No target selected")
	}
	v, ok, _ := c.Find(c.Current)
	if !ok {
		return fmt.Errorf("Unknown target '%s'", c.Current)
	}
	v.Token = token
	return nil
}

func (c *Config) URL() string {
	if v, ok, _ := c.Find(c.Current); ok {
		return v.URL
	}
	return ""
}

func (c *Config) Verified() bool {
	if v, ok, _ := c.Find(c.Current); ok {
		return !v.SkipVerify
	}
	return false
}

func (c *Config) Find(alias string) (*Vault, bool, error) {
	if v, ok := c.Vaults[alias]; ok {
		return v, true, nil
	}

	var v *Vault
	n := 0
	want := strings.TrimSuffix(alias, "/")

	for _, maybe := range c.Vaults {
		if strings.TrimSuffix(maybe.URL, "/") == want {
			n++
			v = maybe
		}
	}
	if n == 1 {
		return v, true, nil
	}
	if n > 1 {
		return nil, true, fmt.Errorf("More than one target for Vault at '%s' (maybe try an alias?)", alias)
	}

	return nil, false, nil
}

func (c *Config) Vault(which string) (*Vault, error) {
	if which == "" {
		which = c.Current
	}

	if which == "" {
		return nil, nil /* not an error */
	}

	v, ok, err := c.Find(which)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("Current target '%s' not found in ~/.saferc", which)
	}
	return v, nil
}
