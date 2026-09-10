package contextbackup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigStatusAndTriggerRoundTrip(t *testing.T) {
	contextPath := filepath.Join(t.TempDir(), "demo")
	config := NewConfig("s3://contexts/demo", 5*time.Minute, 2*time.Second)
	if err := SaveConfig(contextPath, config); err != nil {
		t.Fatal(err)
	}
	gotConfig, err := LoadConfig(contextPath)
	if err != nil {
		t.Fatal(err)
	}
	if gotConfig.Target != config.Target || gotConfig.Interval() != 5*time.Minute || gotConfig.Debounce() != 2*time.Second {
		t.Fatalf("config = %+v, want %+v", gotConfig, config)
	}
	status := Status{PID: 42, Running: true, SourceRevision: "abc", Files: 3, Bytes: 100}
	if err := SaveStatus(contextPath, status); err != nil {
		t.Fatal(err)
	}
	gotStatus, err := LoadStatus(contextPath)
	if err != nil {
		t.Fatal(err)
	}
	if gotStatus.PID != 42 || !gotStatus.Running || gotStatus.SourceRevision != "abc" {
		t.Fatalf("status = %+v", gotStatus)
	}
	if err := Trigger(contextPath); err != nil {
		t.Fatal(err)
	}
	if triggered, err := TriggerTime(contextPath); err != nil || triggered.IsZero() {
		t.Fatalf("trigger time = %v, err = %v", triggered, err)
	}
}

func TestTriggerWithoutConfigurationIsNoop(t *testing.T) {
	contextPath := filepath.Join(t.TempDir(), "demo")
	if err := Trigger(contextPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(contextPath, ".backup")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("trigger created state without config: %v", err)
	}
}
