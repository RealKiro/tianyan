// Package config persists student-side installation settings (uninstall
// password, guardian restart command).
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"

	"tianyan/internal/password"
)

// Student is the persisted student configuration.
type Student struct {
	Password    password.Stored `json:"password,omitempty"`
	ExePath     string          `json:"exe_path,omitempty"`
	Args        []string        `json:"args,omitempty"`
	RebootAfter int             `json:"reboot_after,omitempty"`
}

// Dir returns the per-machine config directory.
func Dir() string {
	if runtime.GOOS == "windows" {
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "tianyan")
	}
	return "/etc/tianyan"
}

// Path returns the config file path.
func Path() string { return filepath.Join(Dir(), "config.json") }

// StopPath is the file that tells a running guardian to exit.
func StopPath() string { return filepath.Join(Dir(), "guardian.stop") }

// Load reads the student config, returning an empty config if missing.
func Load() (Student, error) {
	var c Student
	data, err := os.ReadFile(Path())
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(data, &c)
	return c, err
}

// Save writes the student config, creating the directory as needed.
func Save(c Student) error {
	if err := os.MkdirAll(Dir(), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), data, 0600)
}
