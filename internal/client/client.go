// Package client runs the local-forward supervisor for the client tunnel.
// It is invoked by cli and uses the shared monitor/restart logic.

package client

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

type Client struct {
	cfg    *config.Config
	runner *supervisor.Runner

	localMu sync.Mutex
}

func New(cfg *config.Config) *Client {
	path, err := config.ClientStatePath(cfg)
	if err != nil {
		path = ""
	}
	runner := supervisor.New(restart.ParsePolicy(cfg.Client.RestartPolicy), restart.NewBackoff(cfg.Client.Restart))
	if path != "" {
		runner.SetStateWriter(func(snap statefile.Snapshot) {
			_ = statefile.Write(path, snap)
		})
	}
	return &Client{
		cfg:    cfg,
		runner: runner,
	}
}

func (c *Client) Start() error {
	return c.runner.Start(func() (*exec.Cmd, error) {
		return buildSSHCommand(c.cfg, c.currentLocalForwards())
	})
}

func (c *Client) Stop() error {
	return c.runner.Stop()
}

func (c *Client) State() state.State {
	return c.runner.State()
}

func (c *Client) ConfigSummary() string {
	forwards := config.NormalizeLocalForwards(c.cfg)
	forward := ""
	if len(forwards) > 0 {
		forward = forwards[0]
	}
	host := c.cfg.SSH.Host
	if c.cfg.SSH.User != "" {
		host = fmt.Sprintf("%s@%s", c.cfg.SSH.User, c.cfg.SSH.Host)
	}
	if forward == "" {
		return fmt.Sprintf("%s:%d", host, c.cfg.SSH.Port)
	}
	return fmt.Sprintf("%s:%d (local=%s)", host, c.cfg.SSH.Port, forward)
}

func (c *Client) RunWithLogger(logger *logging.Logger) error {
	opts := supervisor.Options{
		Kind:    "client",
		Summary: c.ConfigSummary,
		MonitorConfig: monitor.Config{
			SleepCheckSec:  c.cfg.Client.SleepCheckSec,
			SleepGapSec:    c.cfg.Client.SleepGapSec,
			NetworkPollSec: c.cfg.Client.NetworkPollSec,
		},
		PeriodicRestartSec: c.cfg.Client.PeriodicRestartSec,
		DebounceMs:         c.cfg.Client.Restart.DebounceMs,
	}
	return c.runner.RunWithLogger(logger, func() (*exec.Cmd, error) {
		return buildSSHCommand(c.cfg, c.currentLocalForwards())
	}, opts)
}

func (c *Client) RequestStop() {
	c.runner.RequestStop()
}

func (c *Client) RequestRestart(reason string) {
	c.runner.RequestRestart(reason, c.cfg.Client.Restart.DebounceMs)
}

func (c *Client) RestartCount() int {
	return c.runner.RestartCount()
}

func (c *Client) LastExitReason() string {
	return c.runner.LastExitReason()
}

func (c *Client) LastSuccess() time.Time {
	return c.runner.LastSuccess()
}

func (c *Client) LastClass() string {
	return c.runner.LastClass()
}

func (c *Client) LastTriggerReason() string {
	return c.runner.LastTriggerReason()
}

func (c *Client) StartSuccessCount() int {
	return c.runner.StartSuccessCount()
}

func (c *Client) StartFailureCount() int {
	return c.runner.StartFailureCount()
}

func (c *Client) ExitSuccessCount() int {
	return c.runner.ExitSuccessCount()
}

func (c *Client) ExitFailureCount() int {
	return c.runner.ExitFailureCount()
}

func (c *Client) CurrentBackoff() time.Duration {
	return c.runner.CurrentBackoff()
}

func (c *Client) currentLocalForwards() []string {
	c.localMu.Lock()
	defer c.localMu.Unlock()
	return config.NormalizeLocalForwards(c.cfg)
}

func (c *Client) LocalForwards() []string {
	return c.currentLocalForwards()
}

func (c *Client) SetLocalForwards(forwards []string) {
	c.localMu.Lock()
	defer c.localMu.Unlock()
	config.SetLocalForwards(c.cfg, forwards)
}

func (c *Client) EnsureLocalForward(forward string) bool {
	trimmed := strings.TrimSpace(forward)
	if trimmed == "" {
		return false
	}
	c.localMu.Lock()
	defer c.localMu.Unlock()
	current := config.NormalizeLocalForwards(c.cfg)
	for _, existing := range current {
		if existing == trimmed {
			return false
		}
	}
	current = append(current, trimmed)
	config.SetLocalForwards(c.cfg, current)
	return true
}

func (c *Client) RemoveLocalForward(forward string) (bool, error) {
	trimmed := strings.TrimSpace(forward)
	if trimmed == "" {
		return false, fmt.Errorf("local forward is required")
	}
	c.localMu.Lock()
	defer c.localMu.Unlock()
	current := config.NormalizeLocalForwards(c.cfg)
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
		return false, fmt.Errorf("at least one local forward is required")
	}
	config.SetLocalForwards(c.cfg, next)
	c.RequestRestart("local forward removed")
	return true, nil
}

func (c *Client) ClearLocalForwards() bool {
	c.localMu.Lock()
	defer c.localMu.Unlock()
	current := config.NormalizeLocalForwards(c.cfg)
	if len(current) == 0 {
		return false
	}
	config.SetLocalForwards(c.cfg, nil)
	c.RequestStop()
	return true
}
