package contextbackup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const stateVersion = 1

type Config struct {
	Version         int    `json:"version"`
	Target          string `json:"target"`
	IntervalSeconds int64  `json:"intervalSeconds"`
	DebounceSeconds int64  `json:"debounceSeconds"`
}

type Status struct {
	Version             int       `json:"version"`
	PID                 int       `json:"pid,omitempty"`
	Running             bool      `json:"running"`
	LastAttempt         time.Time `json:"lastAttempt,omitempty"`
	LastSuccess         time.Time `json:"lastSuccess,omitempty"`
	SourceRevision      string    `json:"sourceRevision,omitempty"`
	Files               int       `json:"files,omitempty"`
	Bytes               int64     `json:"bytes,omitempty"`
	LastError           string    `json:"lastError,omitempty"`
	ConsecutiveFailures int       `json:"consecutiveFailures,omitempty"`
}

func NewConfig(target string, interval, debounce time.Duration) Config {
	return Config{
		Version: stateVersion, Target: target,
		IntervalSeconds: int64(interval / time.Second),
		DebounceSeconds: int64(debounce / time.Second),
	}
}

func (c Config) Interval() time.Duration { return time.Duration(c.IntervalSeconds) * time.Second }
func (c Config) Debounce() time.Duration { return time.Duration(c.DebounceSeconds) * time.Second }

func SaveConfig(contextPath string, config Config) error {
	if config.Version != stateVersion || config.Target == "" || config.IntervalSeconds < 1 || config.DebounceSeconds < 0 {
		return errors.New("invalid backup configuration")
	}
	return writeJSON(filepath.Join(stateDir(contextPath), "config.json"), config)
}

func LoadConfig(contextPath string) (Config, error) {
	var config Config
	if err := readJSON(filepath.Join(stateDir(contextPath), "config.json"), &config); err != nil {
		return Config{}, err
	}
	if config.Version != stateVersion || config.Target == "" || config.IntervalSeconds < 1 || config.DebounceSeconds < 0 {
		return Config{}, errors.New("invalid backup configuration")
	}
	return config, nil
}

func SaveStatus(contextPath string, status Status) error {
	status.Version = stateVersion
	return writeJSON(filepath.Join(stateDir(contextPath), "status.json"), status)
}

func LoadStatus(contextPath string) (Status, error) {
	var status Status
	if err := readJSON(filepath.Join(stateDir(contextPath), "status.json"), &status); err != nil {
		return Status{}, err
	}
	if status.Version != stateVersion {
		return Status{}, errors.New("unsupported backup status version")
	}
	return status, nil
}

func Trigger(contextPath string) error {
	directory := stateDir(contextPath)
	if _, err := os.Stat(filepath.Join(directory, "config.json")); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(directory, "trigger"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	return file.Close()
}

func TriggerTime(contextPath string) (time.Time, error) {
	info, err := os.Stat(filepath.Join(stateDir(contextPath), "trigger"))
	if errors.Is(err, os.ErrNotExist) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

func LogPath(contextPath string) string { return filepath.Join(stateDir(contextPath), "backup.log") }

func stateDir(contextPath string) string { return filepath.Join(contextPath, ".backup") }

func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("read backup state: %w", err)
	}
	return nil
}
