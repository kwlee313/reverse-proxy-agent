// Package cli parses flags and dispatches commands for init, agent control, and IPC queries.
// It is called by cmd/rpa/main.go and coordinates config, logging, IPC, and launchd helpers.

package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"reverse-proxy-agent/internal/agent"
	ipcserver "reverse-proxy-agent/internal/agent/ipc"
	"reverse-proxy-agent/internal/client"
	clientipcserver "reverse-proxy-agent/internal/client/ipc"
	"reverse-proxy-agent/pkg/config"
	ipcclient "reverse-proxy-agent/pkg/ipc/agent"
	ipcclientlocal "reverse-proxy-agent/pkg/ipc/client"
	"reverse-proxy-agent/pkg/launchd"
	"reverse-proxy-agent/pkg/logging"
	"reverse-proxy-agent/pkg/statefile"
)

const (
	exitOK    = 0
	exitUsage = 2
	exitError = 1
)

func Run(args []string) int {
	if len(args) == 0 {
		printUsage()
		return exitUsage
	}

	switch args[0] {
	case "help", "-h", "--help":
		if len(args) > 1 {
			switch args[1] {
			case "agent":
				printAgentUsage()
				return exitOK
			case "client":
				printClientUsage()
				return exitOK
			}
		}
		printUsage()
		return exitOK
	case "init":
		return runInit(args[1:])
	case "agent":
		return runAgent(args[1:])
	case "client":
		return runClient(args[1:])
	case "status":
		return runStatus(args[1:])
	case "logs":
		return runLogs(args[1:])
	case "metrics":
		return runMetrics(args[1:])
	case "doctor":
		return runDoctor(args[1:])
	case "config":
		return runConfig(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		printUsage()
		return exitUsage
	}
}

func runAgent(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "missing agent subcommand (up|down|run|add|remove|clear)")
		printAgentUsage()
		return exitUsage
	}
	switch args[0] {
	case "help", "-h", "--help":
		printAgentUsage()
		return exitOK
	case "up":
		return runAgentUp(args[1:])
	case "down":
		return runAgentDown(args[1:])
	case "run":
		return runAgentRun(args[1:])
	case "add":
		return runAgentAdd(args[1:])
	case "remove":
		return runAgentRemove(args[1:])
	case "clear":
		return runAgentClear(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown agent subcommand: %s\n", args[0])
		return exitUsage
	}
}

func runClient(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "missing client subcommand (up|down|run|add|remove|clear)")
		printClientUsage()
		return exitUsage
	}
	switch args[0] {
	case "help", "-h", "--help":
		printClientUsage()
		return exitOK
	case "up":
		return runClientUp(args[1:])
	case "down":
		return runClientDown(args[1:])
	case "run":
		return runClientRun(args[1:])
	case "add":
		return runClientAdd(args[1:])
	case "remove":
		return runClientRemove(args[1:])
	case "clear":
		return runClientClear(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown client subcommand: %s\n", args[0])
		return exitUsage
	}
}

func runInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", defaultConfigPath(), "path to write config file")
	sshUser := fs.String("ssh-user", "", "ssh username (required)")
	sshHost := fs.String("ssh-host", "", "ssh host (required)")
	sshPort := fs.Int("ssh-port", 22, "ssh port")
	var remoteForwards []string
	var localForwards []string
	sshIdentityFile := fs.String("ssh-identity-file", "~/.ssh/id_ed25519", "ssh identity file")
	agentName := fs.String("agent-name", "rpa-agent", "agent name")
	launchdLabel := fs.String("launchd-label", "com.rpa.agent", "launchd label")
	restartPolicy := fs.String("restart-policy", "always", "restart policy (always|on-failure)")
	periodicRestartSec := fs.Int("periodic-restart-sec", 3600, "periodic restart interval seconds (0 disables)")
	logLevel := fs.String("log-level", "info", "log level")
	logPath := fs.String("log-path", "~/.rpa/logs/agent.log", "log path")
	agentPreventSleep := fs.Bool("agent-prevent-sleep", false, "prevent system sleep while agent is running")
	clientPreventSleep := fs.Bool("client-prevent-sleep", false, "prevent system sleep while client is running")
	force := fs.Bool("force", false, "overwrite config if it exists")
	var sshOptions []string
	fs.Func("ssh-option", "additional ssh option (repeatable)", func(value string) error {
		if strings.TrimSpace(value) == "" {
			return errors.New("ssh-option cannot be empty")
		}
		sshOptions = append(sshOptions, value)
		return nil
	})
	fs.Func("remote-forward", "ssh remote forward spec (repeatable)", func(value string) error {
		if strings.TrimSpace(value) == "" {
			return errors.New("remote-forward cannot be empty")
		}
		remoteForwards = append(remoteForwards, value)
		return nil
	})
	fs.Func("local-forward", "client local forward spec (repeatable)", func(value string) error {
		if strings.TrimSpace(value) == "" {
			return errors.New("local-forward cannot be empty")
		}
		localForwards = append(localForwards, value)
		return nil
	})
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage:")
		fmt.Fprintln(fs.Output(), "  rpa init --ssh-user user --ssh-host host --remote-forward spec [flags]")
		fmt.Fprintln(fs.Output(), "  rpa init --ssh-user user --ssh-host host --local-forward spec [flags]")
		fmt.Fprintln(fs.Output(), "")
		fmt.Fprintln(fs.Output(), "Required:")
		fmt.Fprintln(fs.Output(), "  --ssh-user, --ssh-host")
		fmt.Fprintln(fs.Output(), "  --remote-forward or --local-forward")
		fmt.Fprintln(fs.Output(), "")
		fmt.Fprintln(fs.Output(), "Spec examples:")
		fmt.Fprintln(fs.Output(), "  --remote-forward \"0.0.0.0:2222:localhost:22\"")
		fmt.Fprintln(fs.Output(), "  --local-forward \"127.0.0.1:15432:127.0.0.1:5432\"")
		fmt.Fprintln(fs.Output(), "")
		fmt.Fprintln(fs.Output(), "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	if strings.TrimSpace(*sshUser) == "" || strings.TrimSpace(*sshHost) == "" {
		fmt.Fprintln(os.Stderr, "missing required flags: --ssh-user, --ssh-host")
		fs.Usage()
		return exitUsage
	}
	if len(remoteForwards) == 0 && len(localForwards) == 0 {
		fmt.Fprintln(os.Stderr, "missing required flags: --remote-forward or --local-forward")
		fs.Usage()
		return exitUsage
	}

	if !*force {
		if _, err := os.Stat(*configPath); err == nil {
			fmt.Fprintf(os.Stderr, "config already exists: %s (use --force to overwrite)\n", *configPath)
			return exitUsage
		} else if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "config stat failed: %v\n", err)
			return exitError
		}
	}

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:               *agentName,
			LaunchdLabel:       *launchdLabel,
			RestartPolicy:      *restartPolicy,
			PeriodicRestartSec: *periodicRestartSec,
			PreventSleep:       *agentPreventSleep,
		},
		SSH: config.SSHConfig{
			User:         *sshUser,
			Host:         *sshHost,
			Port:         *sshPort,
			IdentityFile: *sshIdentityFile,
			Options:      sshOptions,
		},
		Logging: config.LoggingConfig{
			Level: *logLevel,
			Path:  *logPath,
		},
	}
	cfg.Client.PreventSleep = *clientPreventSleep
	if len(remoteForwards) > 0 {
		cfg.SSH.RemoteForwards = append([]string(nil), remoteForwards...)
	}
	if len(localForwards) > 0 {
		cfg.Client.LocalForwards = append([]string(nil), localForwards...)
	}
	config.ApplyDefaults(cfg)
	if len(remoteForwards) > 0 {
		if err := config.ValidateAgent(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "config validation failed: %v\n", err)
			return exitError
		}
	}
	if len(localForwards) > 0 {
		if err := config.ValidateClient(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "config validation failed: %v\n", err)
			return exitError
		}
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config marshal failed: %v\n", err)
		return exitError
	}

	if err := ensureDir(filepath.Dir(*configPath)); err != nil {
		fmt.Fprintf(os.Stderr, "create config dir failed: %v\n", err)
		return exitError
	}
	if err := os.WriteFile(*configPath, out, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write config failed: %v\n", err)
		return exitError
	}
	fmt.Printf("config initialized: %s\n", *configPath)
	return exitOK
}

func runAgentUp(args []string) int {
	fs := flag.NewFlagSet("agent up", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}
	if err := config.ValidateAgent(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "config validation failed: %v\n", err)
		return exitError
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve executable: %v\n", err)
		return exitError
	}

	spec := launchd.Spec{
		Label:       cfg.Agent.LaunchdLabel,
		ProgramArgs: []string{exe, "agent", "run", "--config", *configPath},
		RunAtLoad:   true,
		KeepAlive:   true,
		StdoutPath:  "",
		StderrPath:  "",
	}
	if logPath, err := config.LogPath(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "resolve agent log path failed: %v\n", err)
		return exitError
	} else {
		if err := ensureDir(filepath.Dir(logPath)); err != nil {
			fmt.Fprintf(os.Stderr, "create agent log dir failed: %v\n", err)
			return exitError
		}
		spec.StdoutPath = logPath
		spec.StderrPath = logPath
	}
	if cfg.Agent.PreventSleep {
		argv, err := wrapWithCaffeinate(spec.ProgramArgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "caffeinate not available: %v\n", err)
			return exitError
		}
		spec.ProgramArgs = argv
	}
	plistPath, err := launchd.Install(spec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "launchd install failed: %v\n", err)
		return exitError
	}
	if err := launchd.Bootstrap(plistPath); err != nil {
		fmt.Fprintf(os.Stderr, "launchd bootstrap failed: %v\n", err)
		return exitError
	}
	fmt.Printf("agent up: launchd loaded (%s)\n", plistPath)
	if err := waitForServiceReady(cfg, "agent", 3*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "agent up: not ready after 3s: %v\n", err)
		printLaunchdSummary(cfg.Agent.LaunchdLabel)
		_ = printLogFileFallback(cfg, "agent")
		return exitError
	}
	fmt.Println("agent up: ready")
	return exitOK
}

func runAgentDown(args []string) int {
	fs := flag.NewFlagSet("agent down", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}

	plistPath, err := launchd.PlistPath(cfg.Agent.LaunchdLabel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve plist path failed: %v\n", err)
		return exitError
	}
	if err := launchd.Bootout(plistPath); err != nil {
		fmt.Fprintf(os.Stderr, "launchd bootout failed: %v\n", err)
		return exitError
	}
	if _, err := launchd.Uninstall(cfg.Agent.LaunchdLabel); err != nil {
		fmt.Fprintf(os.Stderr, "launchd uninstall failed: %v\n", err)
		return exitError
	}
	fmt.Printf("agent down: launchd unloaded (%s)\n", plistPath)
	return exitOK
}

func runAgentAdd(args []string) int {
	fs := flag.NewFlagSet("agent add", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	remoteForward := fs.String("remote-forward", "", "ssh remote forward spec (required)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if strings.TrimSpace(*remoteForward) == "" {
		fmt.Fprintln(os.Stderr, "remote-forward is required")
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}

	forwards := config.NormalizeRemoteForwards(cfg)
	forwards = append(forwards, *remoteForward)
	config.SetRemoteForwards(cfg, forwards)
	if err := config.Save(*configPath, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "config save failed: %v\n", err)
		return exitError
	}

	if resp, ok, notRunning := tryRuntimeUpdate(func() (*ipcclient.Response, error) {
		return ipcclient.AddRemoteForward(cfg, *remoteForward)
	}); ok {
		if resp.Message != "" {
			fmt.Println(resp.Message)
		}
	} else if notRunning {
		if runAgentUp([]string{"--config", *configPath}) != exitOK {
			return exitError
		}
	}
	return exitOK
}

func runAgentRemove(args []string) int {
	fs := flag.NewFlagSet("agent remove", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	remoteForward := fs.String("remote-forward", "", "ssh remote forward spec (required)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if strings.TrimSpace(*remoteForward) == "" {
		fmt.Fprintln(os.Stderr, "remote-forward is required")
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}

	forwards := config.NormalizeRemoteForwards(cfg)
	next := make([]string, 0, len(forwards))
	for _, value := range forwards {
		if strings.TrimSpace(value) == strings.TrimSpace(*remoteForward) {
			continue
		}
		next = append(next, value)
	}
	if len(next) == 0 {
		fmt.Fprintln(os.Stderr, "at least one remote forward is required")
		return exitError
	}
	config.SetRemoteForwards(cfg, next)
	if err := config.Save(*configPath, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "config save failed: %v\n", err)
		return exitError
	}

	if resp, ok, _ := tryRuntimeUpdate(func() (*ipcclient.Response, error) {
		return ipcclient.RemoveRemoteForward(cfg, *remoteForward)
	}); ok {
		if resp.Message != "" {
			fmt.Println(resp.Message)
		}
	}
	return exitOK
}

func runAgentClear(args []string) int {
	fs := flag.NewFlagSet("agent clear", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}

	forwards := config.NormalizeRemoteForwards(cfg)
	if len(forwards) == 0 {
		fmt.Println("no remote forwards to clear")
	} else {
		config.SetRemoteForwards(cfg, nil)
		if err := config.Save(*configPath, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "config save failed: %v\n", err)
			return exitError
		}
	}

	if resp, ok, _ := tryRuntimeUpdate(func() (*ipcclient.Response, error) {
		return ipcclient.ClearRemoteForwards(cfg)
	}); ok {
		if resp.Message != "" {
			fmt.Println(resp.Message)
		}
	}
	downLaunchdIfPresent(cfg)
	fmt.Println("to start again, run `rpa agent add --remote-forward ...` or `rpa init ...`")
	return exitOK
}

func runClientUp(args []string) int {
	fs := flag.NewFlagSet("client up", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	localForward := fs.String("local-forward", "", "ssh local forward spec (optional)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}

	if strings.TrimSpace(*localForward) != "" {
		forwards := config.NormalizeLocalForwards(cfg)
		forwards = append(forwards, *localForward)
		config.SetLocalForwards(cfg, forwards)
		if err := config.Save(*configPath, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "config save failed: %v\n", err)
			return exitError
		}
	}

	if err := config.ValidateClient(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "config validation failed: %v\n", err)
		return exitError
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve executable: %v\n", err)
		return exitError
	}

	spec := launchd.Spec{
		Label:       cfg.Client.LaunchdLabel,
		ProgramArgs: []string{exe, "client", "run", "--config", *configPath},
		RunAtLoad:   true,
		KeepAlive:   true,
		StdoutPath:  "",
		StderrPath:  "",
	}
	if logPath, err := config.ClientLogPath(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "resolve client log path failed: %v\n", err)
		return exitError
	} else {
		if err := ensureDir(filepath.Dir(logPath)); err != nil {
			fmt.Fprintf(os.Stderr, "create client log dir failed: %v\n", err)
			return exitError
		}
		spec.StdoutPath = logPath
		spec.StderrPath = logPath
	}
	if cfg.Client.PreventSleep {
		argv, err := wrapWithCaffeinate(spec.ProgramArgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "caffeinate not available: %v\n", err)
			return exitError
		}
		spec.ProgramArgs = argv
	}
	plistPath, err := launchd.Install(spec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "launchd install failed: %v\n", err)
		return exitError
	}
	if err := launchd.Bootstrap(plistPath); err != nil {
		fmt.Fprintf(os.Stderr, "launchd bootstrap failed: %v\n", err)
		return exitError
	}
	fmt.Printf("client up: launchd loaded (%s)\n", plistPath)
	if err := waitForServiceReady(cfg, "client", 3*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "client up: not ready after 3s: %v\n", err)
		printLaunchdSummary(cfg.Client.LaunchdLabel)
		_ = printLogFileFallback(cfg, "client")
		return exitError
	}
	fmt.Println("client up: ready")
	return exitOK
}

func runClientDown(args []string) int {
	fs := flag.NewFlagSet("client down", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}

	plistPath, err := launchd.PlistPath(cfg.Client.LaunchdLabel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve plist path failed: %v\n", err)
		return exitError
	}
	if err := launchd.Bootout(plistPath); err != nil {
		fmt.Fprintf(os.Stderr, "launchd bootout failed: %v\n", err)
		return exitError
	}
	if _, err := launchd.Uninstall(cfg.Client.LaunchdLabel); err != nil {
		fmt.Fprintf(os.Stderr, "launchd uninstall failed: %v\n", err)
		return exitError
	}
	fmt.Printf("client down: launchd unloaded (%s)\n", plistPath)
	return exitOK
}

func runClientRun(args []string) int {
	fs := flag.NewFlagSet("client run", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	localForward := fs.String("local-forward", "", "ssh local forward spec (optional)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}
	if strings.TrimSpace(*localForward) != "" {
		config.SetLocalForwards(cfg, []string{*localForward})
	}

	return runForegroundClient(cfg, "client run")
}

func runClientAdd(args []string) int {
	fs := flag.NewFlagSet("client add", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	localForward := fs.String("local-forward", "", "ssh local forward spec (required)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if strings.TrimSpace(*localForward) == "" {
		fmt.Fprintln(os.Stderr, "local-forward is required")
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}

	forwards := config.NormalizeLocalForwards(cfg)
	forwards = append(forwards, *localForward)
	config.SetLocalForwards(cfg, forwards)
	if err := config.Save(*configPath, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "config save failed: %v\n", err)
		return exitError
	}

	if resp, ok, notRunning := tryClientRuntimeUpdate(func() (*ipcclientlocal.Response, error) {
		return ipcclientlocal.AddLocalForward(cfg, *localForward)
	}); ok {
		if resp.Message != "" {
			fmt.Println(resp.Message)
		}
	} else if notRunning {
		if runClientUp([]string{"--config", *configPath}) != exitOK {
			return exitError
		}
	}
	return exitOK
}

func runClientRemove(args []string) int {
	fs := flag.NewFlagSet("client remove", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	localForward := fs.String("local-forward", "", "ssh local forward spec (required)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if strings.TrimSpace(*localForward) == "" {
		fmt.Fprintln(os.Stderr, "local-forward is required")
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}

	forwards := config.NormalizeLocalForwards(cfg)
	next := make([]string, 0, len(forwards))
	for _, value := range forwards {
		if strings.TrimSpace(value) == strings.TrimSpace(*localForward) {
			continue
		}
		next = append(next, value)
	}
	if len(next) == 0 {
		fmt.Fprintln(os.Stderr, "at least one local forward is required")
		return exitError
	}
	config.SetLocalForwards(cfg, next)
	if err := config.Save(*configPath, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "config save failed: %v\n", err)
		return exitError
	}

	if resp, ok, _ := tryClientRuntimeUpdate(func() (*ipcclientlocal.Response, error) {
		return ipcclientlocal.RemoveLocalForward(cfg, *localForward)
	}); ok {
		if resp.Message != "" {
			fmt.Println(resp.Message)
		}
	}
	return exitOK
}

func runClientClear(args []string) int {
	fs := flag.NewFlagSet("client clear", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}

	forwards := config.NormalizeLocalForwards(cfg)
	if len(forwards) == 0 {
		fmt.Println("no local forwards to clear")
	} else {
		config.SetLocalForwards(cfg, nil)
		if err := config.Save(*configPath, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "config save failed: %v\n", err)
			return exitError
		}
	}

	if resp, ok, _ := tryClientRuntimeUpdate(func() (*ipcclientlocal.Response, error) {
		return ipcclientlocal.ClearLocalForwards(cfg)
	}); ok {
		if resp.Message != "" {
			fmt.Println(resp.Message)
		}
	}
	downClientLaunchdIfPresent(cfg)
	fmt.Println("to start again, run `rpa client add --local-forward ...` or `rpa init ...`")
	return exitOK
}

func runClientDoctor(args []string) int {
	fs := flag.NewFlagSet("client doctor", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	localForward := fs.String("local-forward", "", "ssh local forward spec (optional)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}
	if strings.TrimSpace(*localForward) != "" {
		config.SetLocalForwards(cfg, []string{*localForward})
	}

	if err := config.ValidateClient(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "config validation failed: %v\n", err)
		return exitError
	}

	ok := true
	if _, err := exec.LookPath("ssh"); err != nil {
		fmt.Fprintf(os.Stderr, "check ssh binary: FAIL (%v)\n", err)
		ok = false
	} else {
		fmt.Println("check ssh binary: OK")
	}

	if cfg.SSH.IdentityFile != "" {
		path := expandTilde(cfg.SSH.IdentityFile)
		if _, err := os.Stat(path); err != nil {
			fmt.Fprintf(os.Stderr, "check identity file: FAIL (%v)\n", err)
			ok = false
		} else {
			fmt.Println("check identity file: OK")
		}
	}

	if _, err := net.LookupHost(cfg.SSH.Host); err != nil {
		fmt.Fprintf(os.Stderr, "check host resolve: FAIL (%v)\n", err)
		ok = false
	} else {
		fmt.Println("check host resolve: OK")
	}

	forward := firstLocalForward(cfg)
	if forward != "" {
		host, port, err := parseLocalForward(forward)
		if err != nil {
			fmt.Fprintf(os.Stderr, "check local forward: FAIL (%v)\n", err)
			ok = false
		} else {
			ln, err := net.Listen("tcp", net.JoinHostPort(host, port))
			if err != nil {
				fmt.Fprintf(os.Stderr, "check local port availability: FAIL (%v)\n", err)
				ok = false
			} else {
				_ = ln.Close()
				fmt.Println("check local port availability: OK")
			}
		}
	}

	if !ok {
		return exitError
	}
	return exitOK
}

func runClientLogs(args []string) int {
	fs := flag.NewFlagSet("client logs", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}
	return printRecentClientLogs(cfg)
}

func runClientMetrics(args []string) int {
	fs := flag.NewFlagSet("client metrics", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}

	resp, err := ipcclientlocal.Query(cfg, "metrics")
	if err != nil {
		fmt.Fprintf(os.Stderr, "client metrics query failed: %v\n", err)
		return exitError
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "client metrics error: %s\n", resp.Message)
		return exitError
	}
	for k, v := range resp.Data {
		fmt.Printf("%s %s\n", k, v)
	}
	return exitOK
}

func tryClientRuntimeUpdate(fn func() (*ipcclientlocal.Response, error)) (*ipcclientlocal.Response, bool, bool) {
	resp, err := fn()
	if err != nil {
		notRunning := isNotRunning(err)
		if notRunning {
			fmt.Fprintf(os.Stderr, "client not running; starting service: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "client update failed: %v\n", err)
		}
		return nil, false, notRunning
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "client update error: %s\n", resp.Message)
		return resp, false, false
	}
	return resp, true, false
}

func tryRuntimeUpdate(fn func() (*ipcclient.Response, error)) (*ipcclient.Response, bool, bool) {
	resp, err := fn()
	if err != nil {
		notRunning := isNotRunning(err)
		if notRunning {
			fmt.Fprintf(os.Stderr, "agent not running; starting service: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "agent update failed: %v\n", err)
		}
		return nil, false, notRunning
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "agent update error: %s\n", resp.Message)
		return resp, false, false
	}
	return resp, true, false
}

func downLaunchdIfPresent(cfg *config.Config) {
	plistPath, err := launchd.PlistPath(cfg.Agent.LaunchdLabel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve launchd plist failed: %v\n", err)
		return
	}
	if _, err := os.Stat(plistPath); err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "stat launchd plist failed: %v\n", err)
		}
		return
	}
	if err := launchd.Bootout(plistPath); err != nil {
		fmt.Fprintf(os.Stderr, "launchd bootout failed: %v\n", err)
		return
	}
	if _, err := launchd.Uninstall(cfg.Agent.LaunchdLabel); err != nil {
		fmt.Fprintf(os.Stderr, "launchd uninstall failed: %v\n", err)
		return
	}
	fmt.Printf("agent down: launchd unloaded (%s)\n", plistPath)
}

func downClientLaunchdIfPresent(cfg *config.Config) {
	plistPath, err := launchd.PlistPath(cfg.Client.LaunchdLabel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve launchd plist failed: %v\n", err)
		return
	}
	if _, err := os.Stat(plistPath); err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "stat launchd plist failed: %v\n", err)
		}
		return
	}
	if err := launchd.Bootout(plistPath); err != nil {
		fmt.Fprintf(os.Stderr, "launchd bootout failed: %v\n", err)
		return
	}
	if _, err := launchd.Uninstall(cfg.Client.LaunchdLabel); err != nil {
		fmt.Fprintf(os.Stderr, "launchd uninstall failed: %v\n", err)
		return
	}
	fmt.Printf("client down: launchd unloaded (%s)\n", plistPath)
}

func isNotRunning(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "not running")
}

func runAgentRun(args []string) int {
	fs := flag.NewFlagSet("agent run", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}

	return runForegroundAgent(cfg, "agent run")
}

func runForegroundAgent(cfg *config.Config, label string) int {
	if err := config.ValidateAgent(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "config validation failed: %v\n", err)
		return exitError
	}

	agt := agent.New(cfg)
	logs := logging.NewLogBuffer()
	logger, err := logging.NewLogger(cfg, logs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger init failed: %v\n", err)
		return exitError
	}
	startCaffeinate(logger, cfg.Agent.PreventSleep)

	server, err := ipcserver.NewServer(cfg, agt, logs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ipc server init failed: %v\n", err)
		return exitError
	}
	if err := server.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "ipc server start failed: %v\n", err)
		return exitError
	}
	defer server.Stop()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("signal received, stopping")
		agt.RequestStop()
	}()

	fmt.Printf("%s: starting ssh (%s)\n", label, agt.ConfigSummary())
	fmt.Println("note: running until stopped via launchd or Ctrl+C")

	if err := agt.RunWithLogger(logger); err != nil {
		fmt.Fprintf(os.Stderr, "agent exited with error: %v\n", err)
		return exitError
	}
	return exitOK
}

func runForegroundClient(cfg *config.Config, label string) int {
	if err := config.ValidateClient(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "config validation failed: %v\n", err)
		return exitError
	}

	cli := client.New(cfg)
	logs := logging.NewLogBuffer()
	clientLogPath, err := config.ClientLogPath(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve client log path failed: %v\n", err)
		return exitError
	}
	logger, err := logging.NewLoggerWithPath(clientLogPath, logs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger init failed: %v\n", err)
		return exitError
	}
	logger.SetLevel(cfg.ClientLogging.Level)
	startCaffeinate(logger, cfg.Client.PreventSleep)

	server, err := clientipcserver.NewServer(cfg, cli, logs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "client ipc server init failed: %v\n", err)
		return exitError
	}
	if err := server.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "client ipc server start failed: %v\n", err)
		return exitError
	}
	defer server.Stop()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Event("INFO", "signal_received", map[string]any{
			"label": label,
		})
		cli.RequestStop()
	}()

	fmt.Printf("%s: starting ssh (%s)\n", label, cli.ConfigSummary())
	fmt.Println("note: running until stopped via launchd or Ctrl+C")

	if err := cli.RunWithLogger(logger); err != nil {
		fmt.Fprintf(os.Stderr, "client exited with error: %v\n", err)
		return exitError
	}
	printClientAdvice(cli.LastClass())
	return exitOK
}

func runStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}

	agentOK := printStatusBlock("agent", cfg, func() statusPayload {
		resp, err := ipcclient.Query(cfg, "status")
		if err != nil {
			return statusPayload{err: err}
		}
		return statusPayload{ok: resp.OK, message: resp.Message, data: resp.Data}
	})
	clientOK := printStatusBlock("client", cfg, func() statusPayload {
		resp, err := ipcclientlocal.Query(cfg, "status")
		if err != nil {
			return statusPayload{err: err}
		}
		return statusPayload{ok: resp.OK, message: resp.Message, data: resp.Data}
	})
	if !agentOK && !clientOK {
		return exitError
	}
	return exitOK
}

type statusPayload struct {
	ok      bool
	message string
	data    map[string]string
	err     error
}

func printStatusBlock(label string, cfg *config.Config, query func() statusPayload) bool {
	resp := query()
	fmt.Printf("%s:\n", label)
	if resp.err != nil {
		fmt.Printf("  error: %s\n", resp.err.Error())
		if printStatusFallback(label, cfg) {
			return true
		}
		return false
	}
	if !resp.ok {
		fmt.Printf("  error: %s\n", resp.message)
		if printStatusFallback(label, cfg) {
			return true
		}
		return false
	}
	fmt.Printf("  state: %s\n", resp.data["state"])
	fmt.Printf("  summary: %s\n", resp.data["summary"])
	if label == "agent" {
		remoteForwards := strings.TrimSpace(resp.data["remote_forwards"])
		if remoteForwards == "" {
			remoteForwards = strings.Join(config.NormalizeRemoteForwards(cfg), ",")
		}
		if remoteForwards == "" {
			remoteForwards = "(none)"
		}
		fmt.Printf("  remote_forwards: %s\n", remoteForwards)
	} else {
		localForwards := strings.TrimSpace(resp.data["local_forwards"])
		if localForwards == "" {
			localForwards = strings.Join(config.NormalizeLocalForwards(cfg), ",")
		}
		if localForwards == "" {
			localForwards = "(none)"
		}
		fmt.Printf("  local_forwards: %s\n", localForwards)
	}
	fmt.Printf("  uptime: %s\n", resp.data["uptime"])
	fmt.Printf("  restarts: %s\n", resp.data["restarts"])
	fmt.Printf("  last_exit: %s\n", resp.data["last_exit"])
	if v, ok := resp.data["last_class"]; ok && v != "" {
		fmt.Printf("  last_class: %s\n", v)
	}
	if v, ok := resp.data["last_trigger"]; ok && v != "" {
		fmt.Printf("  last_trigger: %s\n", v)
	}
	if v, ok := resp.data["last_success_unix"]; ok && v != "" {
		fmt.Printf("  last_success_unix: %s\n", v)
	}
	if v, ok := resp.data["backoff_ms"]; ok && v != "" {
		fmt.Printf("  backoff_ms: %s\n", v)
	}
	return true
}

func printStatusFallback(label string, cfg *config.Config) bool {
	var path string
	var err error
	switch label {
	case "agent":
		path, err = config.AgentStatePath(cfg)
	case "client":
		path, err = config.ClientStatePath(cfg)
	default:
		return false
	}
	if err != nil {
		return false
	}
	snap, err := statefile.Read(path)
	if err != nil {
		return false
	}
	fmt.Println("  note: using last known state (service not running)")
	if snap.LastExit != "" {
		fmt.Printf("  last_exit: %s\n", snap.LastExit)
	}
	if snap.LastClass != "" {
		fmt.Printf("  last_class: %s\n", snap.LastClass)
	}
	if snap.LastTrigger != "" {
		fmt.Printf("  last_trigger: %s\n", snap.LastTrigger)
	}
	if snap.LastSuccessUnix > 0 {
		fmt.Printf("  last_success_unix: %d\n", snap.LastSuccessUnix)
	}
	if snap.UpdatedUnix > 0 {
		fmt.Printf("  updated_unix: %d\n", snap.UpdatedUnix)
	}
	return true
}

func runLogs(args []string) int {
	target := "agent"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		target = args[0]
		args = args[1:]
	}
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	follow := fs.Bool("follow", false, "follow logs (placeholder)")
	followShort := fs.Bool("f", false, "follow logs (shorthand)")
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}

	switch target {
	case "agent":
		if *follow || *followShort {
			return followLogs(cfg)
		}
		return printRecentLogs(cfg)
	case "client":
		if *follow || *followShort {
			return followClientLogs(cfg)
		}
		return printRecentClientLogs(cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown logs target: %s\n", target)
		return exitUsage
	}
}

func runMetrics(args []string) int {
	target := "agent"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		target = args[0]
		args = args[1:]
	}
	fs := flag.NewFlagSet("metrics", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}

	switch target {
	case "agent":
		resp, err := ipcclient.Query(cfg, "metrics")
		if err != nil {
			fmt.Fprintf(os.Stderr, "metrics query failed: %v\n", err)
			return exitError
		}
		if !resp.OK {
			fmt.Fprintf(os.Stderr, "metrics error: %s\n", resp.Message)
			return exitError
		}
		for k, v := range resp.Data {
			fmt.Printf("%s %s\n", k, v)
		}
		return exitOK
	case "client":
		resp, err := ipcclientlocal.Query(cfg, "metrics")
		if err != nil {
			fmt.Fprintf(os.Stderr, "client metrics query failed: %v\n", err)
			return exitError
		}
		if !resp.OK {
			fmt.Fprintf(os.Stderr, "client metrics error: %s\n", resp.Message)
			return exitError
		}
		for k, v := range resp.Data {
			fmt.Printf("%s %s\n", k, v)
		}
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown metrics target: %s\n", target)
		return exitUsage
	}
}

func runDoctor(args []string) int {
	target := "client"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		target = args[0]
		args = args[1:]
	}
	switch target {
	case "client":
		return runClientDoctor(args)
	case "agent":
		return runAgentDoctor(args)
	default:
		fmt.Fprintf(os.Stderr, "doctor target must be agent or client")
		return exitUsage
	}
}

func runConfig(args []string) int {
	if len(args) == 0 {
		printConfigUsage()
		return exitUsage
	}
	switch args[0] {
	case "get":
		return runConfigGet(args[1:])
	case "set":
		return runConfigSet(args[1:])
	case "show":
		return runConfigShow(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown config subcommand: %s\n", args[0])
		printConfigUsage()
		return exitUsage
	}
}

func runConfigShow(args []string) int {
	fs := flag.NewFlagSet("config show", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config marshal failed: %v\n", err)
		return exitError
	}
	fmt.Print(string(out))
	return exitOK
}

func runConfigGet(args []string) int {
	fs := flag.NewFlagSet("config get", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		printConfigUsage()
		return exitUsage
	}
	key := fs.Arg(0)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}
	value, err := getConfigValue(cfg, key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config get failed: %v\n", err)
		return exitError
	}
	fmt.Println(value)
	return exitOK
}

func runConfigSet(args []string) int {
	fs := flag.NewFlagSet("config set", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 2 {
		printConfigUsage()
		return exitUsage
	}
	key := fs.Arg(0)
	value := fs.Arg(1)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}
	if err := setConfigValue(cfg, key, value); err != nil {
		fmt.Fprintf(os.Stderr, "config set failed: %v\n", err)
		return exitError
	}
	if err := config.Save(*configPath, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "config save failed: %v\n", err)
		return exitError
	}
	fmt.Printf("updated %s\n", key)
	return exitOK
}

func getConfigValue(cfg *config.Config, key string) (string, error) {
	field, err := lookupConfigField(cfg, key)
	if err != nil {
		return "", err
	}
	switch field.Kind() {
	case reflect.String:
		return field.String(), nil
	case reflect.Bool:
		if field.Bool() {
			return "true", nil
		}
		return "false", nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return fmt.Sprintf("%d", field.Int()), nil
	case reflect.Float32, reflect.Float64:
		return fmt.Sprintf("%g", field.Float()), nil
	case reflect.Slice:
		if field.Type().Elem().Kind() == reflect.String {
			out := make([]string, field.Len())
			for i := 0; i < field.Len(); i++ {
				out[i] = field.Index(i).String()
			}
			return strings.Join(out, ","), nil
		}
	}
	return "", fmt.Errorf("unsupported field type for %s", key)
}

func setConfigValue(cfg *config.Config, key, value string) error {
	field, err := lookupConfigField(cfg, key)
	if err != nil {
		return err
	}
	if !field.CanSet() {
		return fmt.Errorf("field %s is not settable", key)
	}
	switch field.Kind() {
	case reflect.String:
		field.SetString(value)
		return nil
	case reflect.Bool:
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid bool for %s", key)
		}
		field.SetBool(parsed)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid int for %s", key)
		}
		field.SetInt(parsed)
		return nil
	case reflect.Float32, reflect.Float64:
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid float for %s", key)
		}
		field.SetFloat(parsed)
		return nil
	case reflect.Slice:
		if field.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("unsupported slice type for %s", key)
		}
		items := splitCSV(value)
		slice := reflect.MakeSlice(field.Type(), len(items), len(items))
		for i, item := range items {
			slice.Index(i).SetString(item)
		}
		field.Set(slice)
		return nil
	default:
		return fmt.Errorf("unsupported field type for %s", key)
	}
}

func lookupConfigField(cfg *config.Config, key string) (reflect.Value, error) {
	parts := strings.Split(key, ".")
	current := reflect.ValueOf(cfg)
	for _, part := range parts {
		if current.Kind() == reflect.Pointer {
			current = current.Elem()
		}
		if current.Kind() != reflect.Struct {
			return reflect.Value{}, fmt.Errorf("invalid key %s", key)
		}
		field, ok := fieldByYAMLTag(current, part)
		if !ok {
			return reflect.Value{}, fmt.Errorf("unknown key %s", key)
		}
		current = field
	}
	return current, nil
}

func fieldByYAMLTag(v reflect.Value, key string) (reflect.Value, bool) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("yaml")
		tag = strings.Split(tag, ",")[0]
		name := strings.ToLower(field.Name)
		if tag == "" {
			tag = name
		}
		if tag == key {
			return v.Field(i), true
		}
	}
	return reflect.Value{}, false
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func runAgentDoctor(args []string) int {
	fs := flag.NewFlagSet("agent doctor", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	remoteForward := fs.String("remote-forward", "", "ssh remote forward spec (optional)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}
	if strings.TrimSpace(*remoteForward) != "" {
		config.SetRemoteForwards(cfg, []string{*remoteForward})
	}

	if err := config.ValidateAgent(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "config validation failed: %v\n", err)
		return exitError
	}

	ok := true
	if _, err := exec.LookPath("ssh"); err != nil {
		fmt.Fprintf(os.Stderr, "check ssh binary: FAIL (%v)\n", err)
		ok = false
	} else {
		fmt.Println("check ssh binary: OK")
	}

	if cfg.SSH.IdentityFile != "" {
		path := expandTilde(cfg.SSH.IdentityFile)
		if _, err := os.Stat(path); err != nil {
			fmt.Fprintf(os.Stderr, "check identity file: FAIL (%v)\n", err)
			ok = false
		} else {
			fmt.Println("check identity file: OK")
		}
	}

	if _, err := net.LookupHost(cfg.SSH.Host); err != nil {
		fmt.Fprintf(os.Stderr, "check host resolve: FAIL (%v)\n", err)
		ok = false
	} else {
		fmt.Println("check host resolve: OK")
	}

	forward := firstRemoteForward(cfg)
	if forward != "" {
		bindHost, bindPort, err := parseRemoteForward(forward)
		if err != nil {
			fmt.Fprintf(os.Stderr, "check remote forward: FAIL (%v)\n", err)
			ok = false
		} else {
			if !isLoopbackHost(bindHost) && !isWildcardHost(bindHost) {
				fmt.Fprintf(os.Stderr, "check remote bind host: WARN (non-local bind %s)\n", bindHost)
			} else {
				fmt.Println("check remote bind host: OK")
			}
			if _, err := strconv.Atoi(bindPort); err != nil {
				fmt.Fprintf(os.Stderr, "check remote bind port: FAIL (%v)\n", err)
				ok = false
			} else {
				fmt.Println("check remote bind port: OK")
			}
		}
	}

	if !ok {
		return exitError
	}
	return exitOK
}

func printRecentLogs(cfg *config.Config) int {
	resp, err := ipcclient.Query(cfg, "logs")
	if err != nil {
		fmt.Fprintf(os.Stderr, "logs query failed: %v\n", err)
		fmt.Fprintln(os.Stderr, "falling back to log file")
		return printLogFileFallback(cfg, "agent")
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "logs error: %s\n", resp.Message)
		fmt.Fprintln(os.Stderr, "falling back to log file")
		return printLogFileFallback(cfg, "agent")
	}
	if len(resp.Logs) == 0 {
		return printLogFileFallback(cfg, "agent")
	}
	for _, line := range resp.Logs {
		fmt.Println(line)
	}
	return exitOK
}

func printRecentClientLogs(cfg *config.Config) int {
	resp, err := ipcclientlocal.Query(cfg, "logs")
	if err != nil {
		fmt.Fprintf(os.Stderr, "client logs query failed: %v\n", err)
		fmt.Fprintln(os.Stderr, "falling back to client log file")
		return printLogFileFallback(cfg, "client")
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "client logs error: %s\n", resp.Message)
		fmt.Fprintln(os.Stderr, "falling back to client log file")
		return printLogFileFallback(cfg, "client")
	}
	if len(resp.Logs) == 0 {
		return printLogFileFallback(cfg, "client")
	}
	for _, line := range resp.Logs {
		fmt.Println(line)
	}
	return exitOK
}

func printLogFileFallback(cfg *config.Config, target string) int {
	var logPath string
	var err error
	switch target {
	case "agent":
		logPath, err = config.LogPath(cfg)
	case "client":
		logPath, err = config.ClientLogPath(cfg)
	default:
		return exitError
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve %s log path failed: %v\n", target, err)
		return exitError
	}
	lines, err := tailLines(logPath, 200)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Printf("no logs (missing log file: %s)\n", logPath)
			return exitOK
		}
		fmt.Fprintf(os.Stderr, "open log file failed: %v\n", err)
		return exitError
	}
	if len(lines) == 0 {
		fmt.Println("no logs")
		return exitOK
	}
	for _, line := range lines {
		fmt.Println(line)
	}
	return exitOK
}

func tailLines(path string, limit int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if limit <= 0 {
		return nil, nil
	}
	lines := make([]string, 0, limit)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if len(lines) >= limit {
			copy(lines, lines[1:])
			lines[len(lines)-1] = line
			continue
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func waitForServiceReady(cfg *config.Config, target string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		switch target {
		case "agent":
			resp, err := ipcclient.Query(cfg, "status")
			if err == nil && resp.OK {
				return nil
			}
			if err != nil {
				lastErr = err
			} else if resp.Message != "" {
				lastErr = errors.New(resp.Message)
			} else {
				lastErr = errors.New("status not ready")
			}
		case "client":
			resp, err := ipcclientlocal.Query(cfg, "status")
			if err == nil && resp.OK {
				return nil
			}
			if err != nil {
				lastErr = err
			} else if resp.Message != "" {
				lastErr = errors.New(resp.Message)
			} else {
				lastErr = errors.New("status not ready")
			}
		default:
			return errors.New("unknown target")
		}
		time.Sleep(250 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = errors.New("timeout waiting for status")
	}
	return lastErr
}

func printLaunchdSummary(label string) {
	output, err := launchd.Print(label)
	if err != nil {
		fmt.Fprintf(os.Stderr, "launchd print failed: %v\n", err)
		if strings.TrimSpace(output) != "" {
			fmt.Fprintln(os.Stderr, tailTextLines(output, 40))
		}
		return
	}
	if strings.TrimSpace(output) == "" {
		return
	}
	fmt.Fprintln(os.Stderr, "launchd status (last 40 lines):")
	fmt.Fprintln(os.Stderr, tailTextLines(output, 40))
}

func tailTextLines(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func followLogs(cfg *config.Config) int {
	logPath, err := config.LogPath(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve log path failed: %v\n", err)
		return exitError
	}
	f, err := os.Open(logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open log file failed: %v\n", err)
		return exitError
	}
	defer f.Close()

	if _, err := f.Seek(0, 2); err != nil {
		fmt.Fprintf(os.Stderr, "seek log file failed: %v\n", err)
		return exitError
	}

	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				time.Sleep(300 * time.Millisecond)
				continue
			}
			fmt.Fprintf(os.Stderr, "read log file failed: %v\n", err)
			return exitError
		}
		fmt.Print(line)
	}
}

func followClientLogs(cfg *config.Config) int {
	logPath, err := config.ClientLogPath(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve client log path failed: %v\n", err)
		return exitError
	}
	f, err := os.Open(logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open client log file failed: %v\n", err)
		return exitError
	}
	defer f.Close()

	if _, err := f.Seek(0, 2); err != nil {
		fmt.Fprintf(os.Stderr, "seek client log file failed: %v\n", err)
		return exitError
	}

	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				time.Sleep(300 * time.Millisecond)
				continue
			}
			fmt.Fprintf(os.Stderr, "read client log file failed: %v\n", err)
			return exitError
		}
		fmt.Print(line)
	}
}

func ensureDir(dir string) error {
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

func expandTilde(path string) string {
	if path == "" || path[0] != '~' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

func firstLocalForward(cfg *config.Config) string {
	forwards := config.NormalizeLocalForwards(cfg)
	if len(forwards) == 0 {
		return ""
	}
	return forwards[0]
}

func firstRemoteForward(cfg *config.Config) string {
	forwards := config.NormalizeRemoteForwards(cfg)
	if len(forwards) == 0 {
		return ""
	}
	return forwards[0]
}

func parseLocalForward(spec string) (string, string, error) {
	parts := strings.Split(spec, ":")
	switch len(parts) {
	case 3:
		return "127.0.0.1", parts[0], nil
	case 4:
		return parts[0], parts[1], nil
	default:
		return "", "", fmt.Errorf("invalid local forward: %s", spec)
	}
}

func parseRemoteForward(spec string) (string, string, error) {
	parts := strings.Split(spec, ":")
	switch len(parts) {
	case 3:
		return "127.0.0.1", parts[0], nil
	case 4:
		return parts[0], parts[1], nil
	default:
		return "", "", fmt.Errorf("invalid remote forward: %s", spec)
	}
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}

func isWildcardHost(host string) bool {
	return host == "0.0.0.0" || host == "::"
}

func printClientAdvice(class string) {
	class = strings.TrimSpace(strings.ToLower(class))
	if class == "" || class == "clean" {
		return
	}
	var msg string
	switch class {
	case "auth":
		msg = "auth failure: check ssh key, permissions, and user"
	case "hostkey":
		msg = "host key failure: run ssh manually to accept the host key"
	case "dns":
		msg = "dns failure: check host name and DNS settings"
	case "network":
		msg = "network failure: check network connectivity"
	case "refused":
		msg = "connection refused: check remote host/port availability"
	case "timeout":
		msg = "connection timed out: check network or firewall settings"
	default:
		msg = "connection failed: check logs for details"
	}
	fmt.Fprintln(os.Stderr, "hint:", msg)
}

func defaultConfigPath() string {
	if fromEnv := strings.TrimSpace(os.Getenv("RPA_CONFIG")); fromEnv != "" {
		return fromEnv
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", "rpa.yaml")
	}
	return filepath.Join(home, ".rpa", "rpa.yaml")
}

func wrapWithCaffeinate(args []string) ([]string, error) {
	path, err := exec.LookPath("caffeinate")
	if err != nil {
		return nil, err
	}
	out := []string{path, "-dimsu"}
	out = append(out, args...)
	return out, nil
}

func startCaffeinate(logger *logging.Logger, enabled bool) {
	if !enabled {
		return
	}
	cmd := exec.Command("caffeinate", "-dimsu", "-w", fmt.Sprintf("%d", os.Getpid()))
	if err := cmd.Start(); err != nil {
		logger.Event("ERROR", "caffeinate_failed", map[string]any{
			"error": err.Error(),
		})
		return
	}
	logger.Event("INFO", "caffeinate_started", map[string]any{
		"pid": fmt.Sprintf("%d", cmd.Process.Pid),
	})
}

func printUsage() {
	fmt.Println("rpa")
	fmt.Println("")
	fmt.Println("Reverse Proxy Agent for resilient SSH tunnels on macOS.")
	fmt.Println("")
	fmt.Println("Usage:")
	fmt.Println("  rpa init [flags]             (write config)")
	fmt.Println("  rpa agent <cmd> [flags]      (remote forwards)")
	fmt.Println("  rpa client <cmd> [flags]     (local forwards)")
	fmt.Println("  rpa status                   (agent + client status)")
	fmt.Println("  rpa logs [agent|client]      (logs, default: agent)")
	fmt.Println("  rpa metrics [agent|client]   (metrics, default: agent)")
	fmt.Println("  rpa doctor [agent|client]    (pre-flight checks)")
	fmt.Println("  rpa config <cmd>             (get/set/show config)")
	fmt.Println("")
	fmt.Println("Quick help:")
	fmt.Println("  rpa init --help")
	fmt.Println("  rpa agent help")
	fmt.Println("  rpa client help")
	fmt.Println("  rpa config help")
	fmt.Println("")
	fmt.Println("Environment:")
	fmt.Println("  RPA_CONFIG overrides the default config path")
	fmt.Println("  Default config path: ~/.rpa/rpa.yaml")
}

func printConfigUsage() {
	fmt.Println("rpa config")
	fmt.Println("")
	fmt.Println("Usage:")
	fmt.Println("  rpa config show [--config rpa.yaml]")
	fmt.Println("  rpa config get <key> [--config rpa.yaml]")
	fmt.Println("  rpa config set <key> <value> [--config rpa.yaml]")
	fmt.Println("")
	fmt.Println("Examples:")
	fmt.Println("  rpa config get agent.prevent_sleep")
	fmt.Println("  rpa config set agent.prevent_sleep true")
	fmt.Println("  rpa config set ssh.options \"ServerAliveInterval=30,ServerAliveCountMax=3\"")
}

func printAgentUsage() {
	fmt.Println("rpa agent")
	fmt.Println("")
	fmt.Println("Agent manages remote forwards and keeps SSH tunnels alive in the background.")
	fmt.Println("")
	fmt.Println("Usage:")
	fmt.Println("  rpa agent up --config rpa.yaml")
	fmt.Println("  rpa agent down --config rpa.yaml")
	fmt.Println("  rpa agent run --config rpa.yaml")
	fmt.Println("  rpa agent add --remote-forward spec --config rpa.yaml")
	fmt.Println("  rpa agent remove --remote-forward spec --config rpa.yaml")
	fmt.Println("  rpa agent clear --config rpa.yaml")
	fmt.Println("")
	fmt.Println("Notes:")
	fmt.Println("  up: install & start launchd service (persisted)")
	fmt.Println("  run: run in foreground for debugging (non-persistent)")
	fmt.Println("  add/remove: updates config and restarts running agent if active")
	fmt.Println("  clear: removes all forwards and stops the service")
	fmt.Println("  sleep prevention is a config flag: agent.prevent_sleep=true")
	fmt.Println("")
	fmt.Println("Remote forward spec example:")
	fmt.Println("  \"0.0.0.0:2222:localhost:22\"  (bind:remotePort:localHost:localPort)")
}

func printClientUsage() {
	fmt.Println("rpa client")
	fmt.Println("")
	fmt.Println("Client manages local forwards and keeps SSH tunnels alive in the background.")
	fmt.Println("")
	fmt.Println("Usage:")
	fmt.Println("  rpa client up --config rpa.yaml [--local-forward spec]")
	fmt.Println("  rpa client down --config rpa.yaml")
	fmt.Println("  rpa client run --config rpa.yaml [--local-forward spec]")
	fmt.Println("  rpa client add --local-forward spec --config rpa.yaml")
	fmt.Println("  rpa client remove --local-forward spec --config rpa.yaml")
	fmt.Println("  rpa client clear --config rpa.yaml")
	fmt.Println("")
	fmt.Println("Notes:")
	fmt.Println("  up: install & start launchd service (persisted)")
	fmt.Println("  run: run in foreground for debugging (non-persistent)")
	fmt.Println("  add/remove: updates config and restarts running client if active")
	fmt.Println("  clear: removes all forwards and stops the service")
	fmt.Println("  sleep prevention is a config flag: client.prevent_sleep=true")
	fmt.Println("  logs/metrics/doctor: use top-level commands (rpa logs|metrics|doctor)")
	fmt.Println("")
	fmt.Println("Local forward spec example:")
	fmt.Println("  \"127.0.0.1:15432:127.0.0.1:5432\" (bind:localPort:remoteHost:remotePort)")
}
