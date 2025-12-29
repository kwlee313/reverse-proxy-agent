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
		fmt.Fprintln(os.Stderr, "missing client subcommand (up|down|run|add|remove|clear|status|logs|metrics|doctor)")
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
	case "status":
		return runClientStatus(args[1:])
	case "logs":
		return runClientLogs(args[1:])
	case "metrics":
		return runClientMetrics(args[1:])
	case "doctor":
		return runClientDoctor(args[1:])
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

	if resp, ok := tryRuntimeUpdate(func() (*ipcclient.Response, error) {
		return ipcclient.AddRemoteForward(cfg, *remoteForward)
	}); ok {
		if resp.Message != "" {
			fmt.Println(resp.Message)
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

	if resp, ok := tryRuntimeUpdate(func() (*ipcclient.Response, error) {
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

	if resp, ok := tryRuntimeUpdate(func() (*ipcclient.Response, error) {
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

	if resp, ok := tryClientRuntimeUpdate(func() (*ipcclientlocal.Response, error) {
		return ipcclientlocal.AddLocalForward(cfg, *localForward)
	}); ok {
		if resp.Message != "" {
			fmt.Println(resp.Message)
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

	if resp, ok := tryClientRuntimeUpdate(func() (*ipcclientlocal.Response, error) {
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

	if resp, ok := tryClientRuntimeUpdate(func() (*ipcclientlocal.Response, error) {
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

func runClientStatus(args []string) int {
	fs := flag.NewFlagSet("client status", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return exitError
	}

	resp, err := ipcclientlocal.Query(cfg, "status")
	if err != nil {
		fmt.Fprintf(os.Stderr, "client status query failed: %v\n", err)
		return exitError
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "client status error: %s\n", resp.Message)
		return exitError
	}
	fmt.Printf("state: %s\n", resp.Data["state"])
	fmt.Printf("summary: %s\n", resp.Data["summary"])
	if v, ok := resp.Data["remote_forwards"]; ok && v != "" {
		fmt.Printf("remote_forwards: %s\n", v)
	}
	fmt.Printf("uptime: %s\n", resp.Data["uptime"])
	fmt.Printf("restarts: %s\n", resp.Data["restarts"])
	fmt.Printf("last_exit: %s\n", resp.Data["last_exit"])
	if v, ok := resp.Data["last_class"]; ok && v != "" {
		fmt.Printf("last_class: %s\n", v)
	}
	if v, ok := resp.Data["last_trigger"]; ok && v != "" {
		fmt.Printf("last_trigger: %s\n", v)
	}
	if v, ok := resp.Data["last_success_unix"]; ok && v != "" {
		fmt.Printf("last_success_unix: %s\n", v)
	}
	if v, ok := resp.Data["backoff_ms"]; ok && v != "" {
		fmt.Printf("backoff_ms: %s\n", v)
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

	resp, err := ipcclientlocal.Query(cfg, "logs")
	if err != nil {
		fmt.Fprintf(os.Stderr, "client logs query failed: %v\n", err)
		return exitError
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "client logs error: %s\n", resp.Message)
		return exitError
	}
	if len(resp.Logs) == 0 {
		fmt.Println("no logs")
		return exitOK
	}
	for _, line := range resp.Logs {
		fmt.Println(line)
	}
	return exitOK
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

func tryClientRuntimeUpdate(fn func() (*ipcclientlocal.Response, error)) (*ipcclientlocal.Response, bool) {
	resp, err := fn()
	if err != nil {
		fmt.Fprintf(os.Stderr, "client not running; changes will apply on next start: %v\n", err)
		return nil, false
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "client update error: %s\n", resp.Message)
		return resp, false
	}
	return resp, true
}

func tryRuntimeUpdate(fn func() (*ipcclient.Response, error)) (*ipcclient.Response, bool) {
	resp, err := fn()
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent not running; changes will apply on next start: %v\n", err)
		return nil, false
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "agent update error: %s\n", resp.Message)
		return resp, false
	}
	return resp, true
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

	resp, err := ipcclient.Query(cfg, "status")
	if err != nil {
		fmt.Fprintf(os.Stderr, "status query failed: %v\n", err)
		return exitError
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "status error: %s\n", resp.Message)
		return exitError
	}
	fmt.Printf("state: %s\n", resp.Data["state"])
	fmt.Printf("summary: %s\n", resp.Data["summary"])
	if v, ok := resp.Data["local_forwards"]; ok && v != "" {
		fmt.Printf("local_forwards: %s\n", v)
	}
	fmt.Printf("uptime: %s\n", resp.Data["uptime"])
	fmt.Printf("restarts: %s\n", resp.Data["restarts"])
	fmt.Printf("last_exit: %s\n", resp.Data["last_exit"])
	if v, ok := resp.Data["last_class"]; ok && v != "" {
		fmt.Printf("last_class: %s\n", v)
	}
	if v, ok := resp.Data["last_success_unix"]; ok && v != "" {
		fmt.Printf("last_success_unix: %s\n", v)
	}
	if v, ok := resp.Data["backoff_ms"]; ok && v != "" {
		fmt.Printf("backoff_ms: %s\n", v)
	}
	return exitOK
}

func runLogs(args []string) int {
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

	if *follow || *followShort {
		return followLogs(cfg)
	}
	return printRecentLogs(cfg)
}

func runMetrics(args []string) int {
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
}

func printRecentLogs(cfg *config.Config) int {
	resp, err := ipcclient.Query(cfg, "logs")
	if err != nil {
		fmt.Fprintf(os.Stderr, "logs query failed: %v\n", err)
		return exitError
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "logs error: %s\n", resp.Message)
		return exitError
	}
	if len(resp.Logs) == 0 {
		fmt.Println("no logs")
		return exitOK
	}
	for _, line := range resp.Logs {
		fmt.Println(line)
	}
	return exitOK
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

func printUsage() {
	fmt.Println("rpa")
	fmt.Println("")
	fmt.Println("Reverse Proxy Agent for resilient SSH tunnels on macOS.")
	fmt.Println("")
	fmt.Println("Usage:")
	fmt.Println("  rpa init [flags]             (write config)")
	fmt.Println("  rpa agent <cmd> [flags]      (remote forwards)")
	fmt.Println("  rpa client <cmd> [flags]     (local forwards)")
	fmt.Println("  rpa status|logs|metrics      (observability)")
	fmt.Println("")
	fmt.Println("Quick help:")
	fmt.Println("  rpa init --help")
	fmt.Println("  rpa agent help")
	fmt.Println("  rpa client help")
	fmt.Println("")
	fmt.Println("Environment:")
	fmt.Println("  RPA_CONFIG overrides the default config path")
	fmt.Println("  Default config path: ~/.rpa/rpa.yaml")
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
	fmt.Println("  rpa client status --config rpa.yaml")
	fmt.Println("  rpa client logs --config rpa.yaml")
	fmt.Println("  rpa client metrics --config rpa.yaml")
	fmt.Println("  rpa client doctor --config rpa.yaml [--local-forward spec]")
	fmt.Println("")
	fmt.Println("Notes:")
	fmt.Println("  up: install & start launchd service (persisted)")
	fmt.Println("  run: run in foreground for debugging (non-persistent)")
	fmt.Println("  add/remove: updates config and restarts running client if active")
	fmt.Println("  clear: removes all forwards and stops the service")
	fmt.Println("")
	fmt.Println("Local forward spec example:")
	fmt.Println("  \"127.0.0.1:15432:127.0.0.1:5432\" (bind:localPort:remoteHost:remotePort)")
}
