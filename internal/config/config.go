// Package config loads settings and holds what belongs to this machine alone.
//
// Local state is never synchronised with the workspace: the promotion mode, the
// ghost cap, which sessions are exposed, and the trust overrides. A workspace
// owner cannot make a member run more processes than that member allows, and
// cannot raise the trust anybody grants on their own machine.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Danielrp551/claudio/internal/daemon"
)

// File names inside the configuration directory.
const (
	FileConfig    = "config.json"
	FileIdentity  = "identity.json"
	FileGhosts    = "ghosts.json"
	FileStatus    = "status.json"
	FileWorkspace = "workspace.json"
)

// Config is everything this machine needs to take part in a workspace.
type Config struct {
	// Workspace is the slug this machine joins.
	Workspace string `json:"workspace,omitempty"`
	// Relay is the endpoint to connect to, for example
	// wss://relay.example.com/connect.
	Relay string `json:"relay,omitempty"`
	// Person is the display name published with this member.
	Person string `json:"person,omitempty"`
	// MachineID is stable for this machine, so reconnecting replaces its
	// sessions rather than adding a second set of them.
	MachineID string `json:"machineId,omitempty"`

	// SessionsDirs are the Claude Code session registries to read. Several
	// profiles commonly point at one directory through a symbolic link, and the
	// registry resolves and deduplicates them.
	SessionsDirs []string `json:"sessionsDirs,omitempty"`

	// Expose names the local sessions this machine shares. An empty list shares
	// nothing, because exposure is opt in.
	Expose []string `json:"expose,omitempty"`

	Policy   daemon.Policy        `json:"policy"`
	Trust    daemon.TrustSettings `json:"trust"`
	LogLevel string               `json:"logLevel,omitempty"`

	dir string
}

// Dir returns the configuration directory, honouring CLAUDIO_CONFIG_DIR.
func Dir() (string, error) {
	if dir := os.Getenv("CLAUDIO_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: locating the home directory: %w", err)
	}
	return filepath.Join(home, ".claudio"), nil
}

// Path returns the location of one file inside the configuration directory.
func Path(name string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// Load reads the configuration, returning defaults when there is no file yet.
func Load() (Config, error) {
	dir, err := Dir()
	if err != nil {
		return Config{}, err
	}
	c := Config{dir: dir}

	raw, err := os.ReadFile(filepath.Join(dir, FileConfig))
	if errors.Is(err, os.ErrNotExist) {
		return c.withDefaults(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("config: reading the configuration: %w", err)
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, fmt.Errorf("config: parsing the configuration: %w", err)
	}
	c.dir = dir
	return c.withDefaults(), nil
}

func (c Config) withDefaults() Config {
	if c.MachineID == "" {
		host, _ := os.Hostname()
		c.MachineID = strings.ToLower(runtime.GOOS + "-" + host)
	}
	if c.Person == "" {
		c.Person = defaultPerson()
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	if c.Policy.Mode == "" {
		c.Policy.Mode = daemon.ModeAuto
	}
	return c
}

func defaultPerson() string {
	for _, v := range []string{"CLAUDIO_PERSON", "USER", "USERNAME"} {
		if name := os.Getenv(v); name != "" {
			return name
		}
	}
	return "someone"
}

// Save writes the configuration back.
func (c Config) Save() error {
	dir := c.dir
	if dir == "" {
		var err error
		if dir, err = Dir(); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: creating %s: %w", dir, err)
	}

	blob, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encoding the configuration: %w", err)
	}

	path := filepath.Join(dir, FileConfig)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		return fmt.Errorf("config: writing the configuration: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("config: installing the configuration: %w", err)
	}
	return nil
}

// Exposes reports whether a local session name is one this machine shares.
//
// The check is exact rather than fuzzy. Sharing a session is a decision, and a
// pattern that accidentally matched one more session than intended would be a
// bad way to find that out.
func (c Config) Exposes(name string) bool {
	for _, e := range c.Expose {
		if e == name {
			return true
		}
	}
	return false
}

// Ready reports what is missing before this machine can join a workspace.
func (c Config) Ready() error {
	var missing []string
	if c.Workspace == "" {
		missing = append(missing, "a workspace")
	}
	if c.Relay == "" {
		missing = append(missing, "a relay URL")
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("config: this machine has not joined a workspace yet, it needs %s, run \"claudio join\"",
		strings.Join(missing, " and "))
}
