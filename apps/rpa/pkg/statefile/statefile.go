// Package statefile stores last-known supervisor state for offline status reporting.
package statefile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Snapshot struct {
	LastExit        string `json:"last_exit,omitempty"`
	LastClass       string `json:"last_class,omitempty"`
	LastTrigger     string `json:"last_trigger,omitempty"`
	LastSuccessUnix int64  `json:"last_success_unix,omitempty"`
	UpdatedUnix     int64  `json:"updated_unix,omitempty"`
}

func Write(path string, snap Snapshot) error {
	if path == "" {
		return fmt.Errorf("state path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	snap.UpdatedUnix = time.Now().Unix()
	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

func Read(path string) (Snapshot, error) {
	if path == "" {
		return Snapshot{}, fmt.Errorf("state path is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, err
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return Snapshot{}, fmt.Errorf("parse state: %w", err)
	}
	return snap, nil
}
