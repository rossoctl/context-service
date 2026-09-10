package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rossoctl/context-service/internal/client"
	"github.com/rossoctl/context-service/internal/contextbackup"
	"github.com/rossoctl/context-service/internal/contextsync"
	"github.com/rossoctl/context-service/internal/localcontext"
)

func backupCommand(c *client.Client, args []string) error {
	if len(args) == 0 {
		showHelp([]string{"context", "backup"})
		return errors.New("a backup command is required")
	}
	switch args[0] {
	case "start":
		return startBackup(args[1:])
	case "stop":
		return stopBackup(args[1:])
	case "status":
		return showBackupStatus(args[1:])
	case "run":
		return runBackup(c, args[1:])
	default:
		return fmt.Errorf("unknown backup command %q; run 'contextctl help context backup'", args[0])
	}
}

func startBackup(args []string) error {
	flags := flag.NewFlagSet("context backup start", flag.ContinueOnError)
	target := flags.String("to", "", "backup target: s3://bucket/prefix or pvc://namespace/name")
	interval := flags.Duration("interval", 5*time.Minute, "periodic backup interval")
	debounce := flags.Duration("debounce", 2*time.Second, "delay after capture events")
	flags.Usage = func() { showHelp([]string{"context", "backup", "start"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	setFlags := map[string]bool{}
	flags.Visit(func(item *flag.Flag) { setFlags[item.Name] = true })
	manifest, err := localContextStore().Get(name)
	if err != nil {
		return err
	}
	if *target == "" {
		if existing, loadErr := contextbackup.LoadConfig(manifest.Path); loadErr == nil {
			*target = existing.Target
			if !setFlags["interval"] {
				*interval = existing.Interval()
			}
			if !setFlags["debounce"] {
				*debounce = existing.Debounce()
			}
		} else {
			return errors.New("--to is required the first time backup is started")
		}
	}
	if *interval < time.Second || *debounce < 0 {
		return errors.New("--interval must be at least 1s and --debounce cannot be negative")
	}
	if err := validateBackupTarget(*target); err != nil {
		return err
	}
	if status, statusErr := contextbackup.LoadStatus(manifest.Path); statusErr == nil && processRunning(status.PID) {
		return fmt.Errorf("backup is already running for %s (pid %d)", name, status.PID)
	}
	_ = os.Remove(filepath.Join(manifest.Path, ".backup", "worker.lock"))
	config := contextbackup.NewConfig(*target, *interval, *debounce)
	if err := contextbackup.SaveConfig(manifest.Path, config); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(contextbackup.LogPath(manifest.Path), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	command := exec.Command(executable, "ctx", "backup", "run", name)
	command.Env = append(os.Environ(), "CS_CONTEXT_HOME="+filepath.Dir(manifest.Path))
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return fmt.Errorf("start backup process: %w", err)
	}
	pid := command.Process.Pid
	if err := command.Process.Release(); err != nil {
		return fmt.Errorf("release backup process: %w", err)
	}
	started := false
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		status, statusErr := contextbackup.LoadStatus(manifest.Path)
		if statusErr == nil && status.PID == pid && status.Running {
			started = true
			break
		}
		if !processRunning(pid) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !started {
		return fmt.Errorf("backup process failed to start; see %s", contextbackup.LogPath(manifest.Path))
	}
	fmt.Printf("Backup started for %s → %s\n", name, *target)
	fmt.Printf("Every %s · pid %d\n", interval.String(), pid)
	return nil
}

func stopBackup(args []string) error {
	flags := flag.NewFlagSet("context backup stop", flag.ContinueOnError)
	flags.Usage = func() { showHelp([]string{"context", "backup", "stop"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	manifest, err := localContextStore().Get(name)
	if err != nil {
		return err
	}
	status, err := contextbackup.LoadStatus(manifest.Path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("backup is not running for %s", name)
	}
	if err != nil {
		return err
	}
	if !processRunning(status.PID) {
		return fmt.Errorf("backup is not running for %s", name)
	}
	process, err := os.FindProcess(status.PID)
	if err != nil {
		return err
	}
	if err := process.Signal(os.Interrupt); err != nil {
		return fmt.Errorf("stop backup process: %w", err)
	}
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if !processRunning(status.PID) {
			fmt.Printf("Backup stopped for %s\n", name)
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("backup process %d did not stop", status.PID)
}

func showBackupStatus(args []string) error {
	flags := flag.NewFlagSet("context backup status", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "backup", "status"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	manifest, err := localContextStore().Get(name)
	if err != nil {
		return err
	}
	config, err := contextbackup.LoadConfig(manifest.Path)
	if err != nil {
		return fmt.Errorf("backup is not configured for %s", name)
	}
	status, err := contextbackup.LoadStatus(manifest.Path)
	if errors.Is(err, os.ErrNotExist) {
		status = contextbackup.Status{}
	} else if err != nil {
		return err
	}
	status.Running = processRunning(status.PID)
	if *jsonOutput {
		value := struct {
			Name   string               `json:"name"`
			Config contextbackup.Config `json:"config"`
			Status contextbackup.Status `json:"status"`
		}{name, config, status}
		encoded, _ := json.MarshalIndent(value, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	state := "Stopped"
	if status.Running {
		state = "Running"
	}
	fmt.Printf("%s  %s · every %s\n", name, state, config.Interval())
	fmt.Printf("└── %s\n", config.Target)
	if !status.LastAttempt.IsZero() {
		fmt.Printf("    last attempt  %s\n", status.LastAttempt.Local().Format(time.RFC3339))
	}
	if !status.LastSuccess.IsZero() {
		fmt.Printf("    last success  %s · %d files · %s\n", status.LastSuccess.Local().Format(time.RFC3339), status.Files, formatBytes(status.Bytes))
	}
	if status.SourceRevision != "" {
		fmt.Printf("    revision      %s\n", status.SourceRevision)
	}
	if status.LastError != "" {
		fmt.Printf("    last error    %s\n", status.LastError)
	}
	fmt.Printf("    log           %s\n", contextbackup.LogPath(manifest.Path))
	return nil
}

func runBackup(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context backup run", flag.ContinueOnError)
	flags.Usage = func() { showHelp([]string{"context", "backup", "run"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	store := localContextStore()
	manifest, err := store.Get(name)
	if err != nil {
		return err
	}
	config, err := contextbackup.LoadConfig(manifest.Path)
	if err != nil {
		return err
	}
	lockPath := filepath.Join(manifest.Path, ".backup", "worker.lock")
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		previous, _ := contextbackup.LoadStatus(manifest.Path)
		if processRunning(previous.PID) {
			return errors.New("another backup process is already running")
		}
		if removeErr := os.Remove(lockPath); removeErr != nil {
			return errors.New("remove stale backup lock")
		}
		if err := os.Mkdir(lockPath, 0o700); err != nil {
			return errors.New("acquire backup lock")
		}
	}
	defer os.Remove(lockPath)

	status, _ := contextbackup.LoadStatus(manifest.Path)
	status.PID = os.Getpid()
	status.Running = true
	_ = contextbackup.SaveStatus(manifest.Path, status)
	defer func() {
		status.Running = false
		status.PID = 0
		_ = contextbackup.SaveStatus(manifest.Path, status)
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
	next := time.Now()
	lastTrigger, _ := contextbackup.TriggerTime(manifest.Path)
	for {
		now := time.Now()
		if !now.Before(next) {
			status = backupAttempt(ctx, c, store, name, config, status)
			_ = contextbackup.SaveStatus(manifest.Path, status)
			if status.LastError == "" {
				next = time.Now().Add(config.Interval())
			} else {
				next = time.Now().Add(retryDelay(status.ConsecutiveFailures, config.Interval()))
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-poll.C:
			triggered, triggerErr := contextbackup.TriggerTime(manifest.Path)
			if triggerErr == nil && triggered.After(lastTrigger) {
				lastTrigger = triggered
				next = time.Now().Add(config.Debounce())
			}
		}
	}
}

func backupAttempt(ctx context.Context, c *client.Client, store *localcontext.Store, name string, config contextbackup.Config, status contextbackup.Status) contextbackup.Status {
	status.LastAttempt = time.Now().UTC()
	revision, err := store.Revision(name)
	if err == nil && revision.Digest != status.SourceRevision {
		err = backupPush(ctx, c, name, config.Target)
	}
	if err != nil {
		status.LastError = err.Error()
		status.ConsecutiveFailures++
		return status
	}
	status.LastSuccess = time.Now().UTC()
	status.SourceRevision = revision.Digest
	status.Files = revision.Files
	status.Bytes = revision.Bytes
	status.LastError = ""
	status.ConsecutiveFailures = 0
	return status
}

func pushBackupTarget(ctx context.Context, c *client.Client, name, target string) error {
	if strings.HasPrefix(target, "s3://") {
		return pushContextWithContext(ctx, c, []string{name, "--to", target})
	}
	value := strings.TrimPrefix(target, "pvc://")
	namespace, remoteName, found := strings.Cut(value, "/")
	if !found {
		return fmt.Errorf("invalid PVC backup target %q", target)
	}
	return pushContextWithContext(ctx, c, []string{name, "--remote-name", remoteName, "--namespace", namespace})
}

var backupPush = pushBackupTarget

func validateBackupTarget(target string) error {
	if strings.HasPrefix(target, "s3://") {
		_, err := contextsync.ParseS3Location(target)
		return err
	}
	if strings.HasPrefix(target, "pvc://") {
		value := strings.TrimPrefix(target, "pvc://")
		namespace, name, found := strings.Cut(value, "/")
		if found && namespace != "" && name != "" && !strings.Contains(name, "/") {
			return nil
		}
	}
	return fmt.Errorf("invalid backup target %q: use s3://bucket/prefix or pvc://namespace/name", target)
}

func retryDelay(failures int, interval time.Duration) time.Duration {
	if failures < 1 {
		failures = 1
	}
	shift := failures - 1
	if shift > 4 {
		shift = 4
	}
	delay := 5 * time.Second * time.Duration(1<<shift)
	if delay > time.Minute {
		delay = time.Minute
	}
	if interval < delay {
		return interval
	}
	return delay
}

func processRunning(pid int) bool {
	if pid < 1 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

func notifyBackup(store *localcontext.Store, name string) error {
	manifest, err := store.Get(name)
	if err != nil {
		return err
	}
	return contextbackup.Trigger(manifest.Path)
}
