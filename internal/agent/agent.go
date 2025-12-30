// Package agent runs the supervisor loop that manages ssh lifecycle and restarts.
// It is invoked by cli and reports status via the ipc server.

package agent

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"reverse-proxy-agent/internal/supervisor"
	"reverse-proxy-agent/pkg/config"
	"reverse-proxy-agent/pkg/logging"
	"reverse-proxy-agent/pkg/monitor"
	"reverse-proxy-agent/pkg/restart"
	"reverse-proxy-agent/pkg/state"
	"reverse-proxy-agent/pkg/statefile"
)

type Agent struct {
	cfg    *config.Config
	runner *supervisor.Runner

	forwardMu sync.Mutex
}

func New(cfg *config.Config) *Agent {
	path, err := config.AgentStatePath(cfg)
	if err != nil {
		path = ""
	}
	runner := supervisor.New(restart.ParsePolicy(cfg.Agent.RestartPolicy), restart.NewBackoff(cfg.Agent.Restart))
	if path != "" {
		runner.SetStateWriter(func(snap statefile.Snapshot) {
			_ = statefile.Write(path, snap)
		})
	}
	return &Agent{
		cfg:    cfg,
		runner: runner,
	}
}

func (a *Agent) Start() error {
	return a.runner.Start(func() (*exec.Cmd, error) {
		return buildSSHCommand(a.cfg, a.currentRemoteForwards())
	})
}

func (a *Agent) Stop() error {
	return a.runner.Stop()
}

func (a *Agent) State() state.State {
	return a.runner.State()
}

func (a *Agent) ConfigSummary() string {
	return fmt.Sprintf("%s@%s:%d", a.cfg.SSH.User, a.cfg.SSH.Host, a.cfg.SSH.Port)
}

func (a *Agent) RunWithLogger(logger *logging.Logger) error {
	opts := supervisor.Options{
		Kind:    "agent",
		Summary: a.ConfigSummary,
		MonitorConfig: monitor.Config{
			SleepCheckSec:  a.cfg.Agent.SleepCheckSec,
			SleepGapSec:    a.cfg.Agent.SleepGapSec,
			NetworkPollSec: a.cfg.Agent.NetworkPollSec,
		},
		PeriodicRestartSec: a.cfg.Agent.PeriodicRestartSec,
		DebounceMs:         a.cfg.Agent.Restart.DebounceMs,
	}
	return a.runner.RunWithLogger(logger, func() (*exec.Cmd, error) {
		return buildSSHCommand(a.cfg, a.currentRemoteForwards())
	}, opts)
}

func (a *Agent) RequestStop() {
	a.runner.RequestStop()
}

func (a *Agent) RequestRestart(reason string) {
	a.runner.RequestRestart(reason, a.cfg.Agent.Restart.DebounceMs)
}

func (a *Agent) RestartCount() int {
	return a.runner.RestartCount()
}

func (a *Agent) LastExitReason() string {
	return a.runner.LastExitReason()
}

func (a *Agent) LastSuccess() time.Time {
	return a.runner.LastSuccess()
}

func (a *Agent) LastClass() string {
	return a.runner.LastClass()
}

func (a *Agent) LastTriggerReason() string {
	return a.runner.LastTriggerReason()
}

func (a *Agent) StartSuccessCount() int {
	return a.runner.StartSuccessCount()
}

func (a *Agent) StartFailureCount() int {
	return a.runner.StartFailureCount()
}

func (a *Agent) ExitSuccessCount() int {
	return a.runner.ExitSuccessCount()
}

func (a *Agent) ExitFailureCount() int {
	return a.runner.ExitFailureCount()
}

func (a *Agent) CurrentBackoff() time.Duration {
	return a.runner.CurrentBackoff()
}

func (a *Agent) AddRemoteForward(forward string) (bool, error) {
	trimmed := strings.TrimSpace(forward)
	if trimmed == "" {
		return false, fmt.Errorf("remote forward is required")
	}
	a.forwardMu.Lock()
	defer a.forwardMu.Unlock()
	current := config.NormalizeRemoteForwards(a.cfg)
	for _, existing := range current {
		if existing == trimmed {
			return false, nil
		}
	}
	current = append(current, trimmed)
	config.SetRemoteForwards(a.cfg, current)
	a.RequestRestart("remote forward added")
	return true, nil
}

func (a *Agent) RemoveRemoteForward(forward string) (bool, error) {
	trimmed := strings.TrimSpace(forward)
	if trimmed == "" {
		return false, fmt.Errorf("remote forward is required")
	}
	a.forwardMu.Lock()
	defer a.forwardMu.Unlock()
	current := config.NormalizeRemoteForwards(a.cfg)
	next := make([]string, 0, len(current))
	removed := false
	for _, existing := range current {
		if existing == trimmed {
			removed = true
			continue
		}
		next = append(next, existing)
	}
	if !removed {
		return false, nil
	}
	if len(next) == 0 {
		return false, fmt.Errorf("at least one remote forward is required")
	}
	config.SetRemoteForwards(a.cfg, next)
	a.RequestRestart("remote forward removed")
	return true, nil
}

func (a *Agent) ClearRemoteForwards() bool {
	a.forwardMu.Lock()
	defer a.forwardMu.Unlock()
	current := config.NormalizeRemoteForwards(a.cfg)
	if len(current) == 0 {
		return false
	}
	config.SetRemoteForwards(a.cfg, nil)
	a.RequestStop()
	return true
}

func (a *Agent) currentRemoteForwards() []string {
	a.forwardMu.Lock()
	defer a.forwardMu.Unlock()
	return config.NormalizeRemoteForwards(a.cfg)
}

func (a *Agent) RemoteForwards() []string {
	return a.currentRemoteForwards()
}
