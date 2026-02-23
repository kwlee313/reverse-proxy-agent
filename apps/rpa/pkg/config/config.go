// Package config loads YAML config, applies defaults, and validates required fields.
// It is used by cli, agent, logging, and ipc to resolve runtime settings and paths.

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Agent         AgentConfig   `yaml:"agent"`
	Client        ClientConfig  `yaml:"client"`
	SSH           SSHConfig     `yaml:"ssh"`
	Logging       LoggingConfig `yaml:"logging"`
	ClientLogging LoggingConfig `yaml:"client_logging"`
}

type AgentConfig struct {
	Name               string        `yaml:"name"`
	LaunchdLabel       string        `yaml:"launchd_label"`
	RestartPolicy      string        `yaml:"restart_policy"`
	Restart            RestartConfig `yaml:"restart"`
	PeriodicRestartSec int           `yaml:"periodic_restart_sec"`
	SleepCheckSec      int           `yaml:"sleep_check_sec"`
	SleepGapSec        int           `yaml:"sleep_gap_sec"`
	NetworkPollSec     int           `yaml:"network_poll_sec"`
	PreventSleep       bool          `yaml:"prevent_sleep"`
}

type ClientConfig struct {
	Name               string        `yaml:"name"`
	LaunchdLabel       string        `yaml:"launchd_label"`
	RestartPolicy      string        `yaml:"restart_policy"`
	Restart            RestartConfig `yaml:"restart"`
	PeriodicRestartSec int           `yaml:"periodic_restart_sec"`
	SleepCheckSec      int           `yaml:"sleep_check_sec"`
	SleepGapSec        int           `yaml:"sleep_gap_sec"`
	NetworkPollSec     int           `yaml:"network_poll_sec"`
	LocalForwards      []string      `yaml:"local_forwards"`
	PreventSleep       bool          `yaml:"prevent_sleep"`
}

type clientConfigRaw struct {
	Name               string        `yaml:"name"`
	LaunchdLabel       string        `yaml:"launchd_label"`
	RestartPolicy      string        `yaml:"restart_policy"`
	Restart            RestartConfig `yaml:"restart"`
	PeriodicRestartSec int           `yaml:"periodic_restart_sec"`
	SleepCheckSec      int           `yaml:"sleep_check_sec"`
	SleepGapSec        int           `yaml:"sleep_gap_sec"`
	NetworkPollSec     int           `yaml:"network_poll_sec"`
	LocalForward       string        `yaml:"local_forward"`
	LocalForwards      []string      `yaml:"local_forwards"`
	PreventSleep       bool          `yaml:"prevent_sleep"`
}

func (c *ClientConfig) UnmarshalYAML(value *yaml.Node) error {
	var raw clientConfigRaw
	if err := value.Decode(&raw); err != nil {
		return err
	}
	*c = ClientConfig{
		Name:               raw.Name,
		LaunchdLabel:       raw.LaunchdLabel,
		RestartPolicy:      raw.RestartPolicy,
		Restart:            raw.Restart,
		PeriodicRestartSec: raw.PeriodicRestartSec,
		SleepCheckSec:      raw.SleepCheckSec,
		SleepGapSec:        raw.SleepGapSec,
		NetworkPollSec:     raw.NetworkPollSec,
		LocalForwards:      mergeLocalForwards(raw.LocalForward, raw.LocalForwards),
		PreventSleep:       raw.PreventSleep,
	}
	return nil
}

type SSHConfig struct {
	User           string   `yaml:"user"`
	Host           string   `yaml:"host"`
	Port           int      `yaml:"port"`
	RemoteForwards []string `yaml:"remote_forwards"`
	IdentityFile   string   `yaml:"identity_file"`
	Options        []string `yaml:"options"`
	CheckSec       int      `yaml:"check_sec"`
}

type LoggingConfig struct {
	Level string `yaml:"level"`
	Path  string `yaml:"path"`
}

type RestartConfig struct {
	MinDelayMs int     `yaml:"min_delay_ms"`
	MaxDelayMs int     `yaml:"max_delay_ms"`
	Factor     float64 `yaml:"factor"`
	Jitter     float64 `yaml:"jitter"`
	DebounceMs int     `yaml:"debounce_ms"`
}

func Load(path string) (*Config, error) {
	if path == "" {
		return nil, errors.New("config path is empty")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}

	applyDefaults(&cfg)
	return &cfg, nil
}

func ApplyDefaults(cfg *Config) {
	if cfg == nil {
		return
	}
	applyDefaults(cfg)
}

func applyDefaults(cfg *Config) {
	if cfg.Agent.Name == "" {
		cfg.Agent.Name = "rpa-agent"
	}
	if cfg.Agent.LaunchdLabel == "" {
		cfg.Agent.LaunchdLabel = "com.rpa.agent"
	}
	if cfg.Agent.RestartPolicy == "" {
		cfg.Agent.RestartPolicy = "always"
	}
	if cfg.Agent.PeriodicRestartSec < 0 {
		cfg.Agent.PeriodicRestartSec = 0
	}
	if cfg.Agent.SleepCheckSec == 0 {
		cfg.Agent.SleepCheckSec = 5
	}
	if cfg.Agent.SleepGapSec == 0 {
		cfg.Agent.SleepGapSec = 30
	}
	if cfg.Agent.NetworkPollSec == 0 {
		cfg.Agent.NetworkPollSec = 5
	}
	if cfg.Agent.Restart.MinDelayMs == 0 {
		cfg.Agent.Restart.MinDelayMs = 2000
	}
	if cfg.Agent.Restart.MaxDelayMs == 0 {
		cfg.Agent.Restart.MaxDelayMs = 30000
	}
	if cfg.Agent.Restart.Factor == 0 {
		cfg.Agent.Restart.Factor = 2.0
	}
	if cfg.Agent.Restart.Jitter == 0 {
		cfg.Agent.Restart.Jitter = 0.2
	}
	if cfg.Agent.Restart.DebounceMs == 0 {
		cfg.Agent.Restart.DebounceMs = 2000
	}
	if cfg.Client.Name == "" {
		cfg.Client.Name = "rpa-client"
	}
	if cfg.Client.LaunchdLabel == "" {
		cfg.Client.LaunchdLabel = "com.rpa.client"
	}
	if cfg.Client.RestartPolicy == "" {
		cfg.Client.RestartPolicy = "always"
	}
	if cfg.Client.PeriodicRestartSec < 0 {
		cfg.Client.PeriodicRestartSec = 0
	}
	if cfg.Client.SleepCheckSec == 0 {
		cfg.Client.SleepCheckSec = 5
	}
	if cfg.Client.SleepGapSec == 0 {
		cfg.Client.SleepGapSec = 30
	}
	if cfg.Client.NetworkPollSec == 0 {
		cfg.Client.NetworkPollSec = 5
	}
	if cfg.Client.Restart.MinDelayMs == 0 {
		cfg.Client.Restart.MinDelayMs = 2000
	}
	if cfg.Client.Restart.MaxDelayMs == 0 {
		cfg.Client.Restart.MaxDelayMs = 30000
	}
	if cfg.Client.Restart.Factor == 0 {
		cfg.Client.Restart.Factor = 2.0
	}
	if cfg.Client.Restart.Jitter == 0 {
		cfg.Client.Restart.Jitter = 0.2
	}
	if cfg.Client.Restart.DebounceMs == 0 {
		cfg.Client.Restart.DebounceMs = 2000
	}
	if cfg.SSH.Port == 0 {
		cfg.SSH.Port = 22
	}
	if cfg.SSH.CheckSec == 0 {
		cfg.SSH.CheckSec = 5
	}
	if cfg.SSH.Options == nil {
		cfg.SSH.Options = []string{}
	}
	ensureSSHOption(&cfg.SSH.Options, "ServerAliveInterval=30")
	ensureSSHOption(&cfg.SSH.Options, "ServerAliveCountMax=3")
	ensureSSHOption(&cfg.SSH.Options, "StrictHostKeyChecking=accept-new")
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Logging.Path == "" {
		cfg.Logging.Path = "~/.rpa/logs/agent.log"
	}
	if cfg.ClientLogging.Level == "" {
		cfg.ClientLogging.Level = "info"
	}
	if cfg.ClientLogging.Path == "" {
		cfg.ClientLogging.Path = "~/.rpa/logs/client.log"
	}
}

func ensureSSHOption(options *[]string, value string) {
	key := optionKey(value)
	if key == "" {
		return
	}
	for _, opt := range *options {
		if optionKey(opt) == key {
			return
		}
	}
	*options = append(*options, value)
}

func optionKey(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if idx := strings.IndexAny(trimmed, " ="); idx >= 0 {
		return strings.ToLower(trimmed[:idx])
	}
	return strings.ToLower(trimmed)
}

func ValidateAgent(cfg *Config) error {
	if err := validateCommon(cfg); err != nil {
		return err
	}
	if len(NormalizeRemoteForwards(cfg)) == 0 {
		return errors.New("ssh.remote_forwards is required")
	}
	return validateSupervisor(cfg.Agent.RestartPolicy, cfg.Agent.Restart, cfg.Agent.PeriodicRestartSec, cfg.Agent.SleepCheckSec, cfg.Agent.SleepGapSec, cfg.Agent.NetworkPollSec, "agent")
}

func ValidateClient(cfg *Config) error {
	if err := validateCommon(cfg); err != nil {
		return err
	}
	if len(NormalizeLocalForwards(cfg)) == 0 {
		return errors.New("client.local_forwards is required")
	}
	return validateSupervisor(cfg.Client.RestartPolicy, cfg.Client.Restart, cfg.Client.PeriodicRestartSec, cfg.Client.SleepCheckSec, cfg.Client.SleepGapSec, cfg.Client.NetworkPollSec, "client")
}

func validateCommon(cfg *Config) error {
	if cfg == nil {
		return errors.New("config is nil")
	}
	if strings.TrimSpace(cfg.SSH.Host) == "" {
		return errors.New("ssh.host is required")
	}
	if strings.TrimSpace(cfg.SSH.User) == "" {
		return errors.New("ssh.user is required")
	}
	if cfg.SSH.Port <= 0 {
		return fmt.Errorf("ssh.port must be > 0 (got %d)", cfg.SSH.Port)
	}
	if cfg.SSH.CheckSec < 0 {
		return fmt.Errorf("ssh.check_sec must be >= 0 (got %d)", cfg.SSH.CheckSec)
	}
	return nil
}

func validateSupervisor(policy string, restartCfg RestartConfig, periodic, sleepCheck, sleepGap, networkPoll int, label string) error {
	switch strings.ToLower(policy) {
	case "always", "on-failure":
	default:
		return fmt.Errorf("%s.restart_policy must be always or on-failure (got %q)", label, policy)
	}
	if restartCfg.MinDelayMs < 0 || restartCfg.MaxDelayMs < 0 {
		return fmt.Errorf("%s.restart min/max delay must be >= 0", label)
	}
	if restartCfg.MaxDelayMs > 0 && restartCfg.MinDelayMs > restartCfg.MaxDelayMs {
		return fmt.Errorf("%s.restart min delay must be <= max delay", label)
	}
	if restartCfg.Factor < 1.0 {
		return fmt.Errorf("%s.restart factor must be >= 1.0", label)
	}
	if restartCfg.Jitter < 0 || restartCfg.Jitter > 1.0 {
		return fmt.Errorf("%s.restart jitter must be between 0 and 1", label)
	}
	if restartCfg.DebounceMs < 0 {
		return fmt.Errorf("%s.restart debounce_ms must be >= 0", label)
	}
	if periodic < 0 {
		return fmt.Errorf("%s.periodic_restart_sec must be >= 0", label)
	}
	if sleepCheck < 0 {
		return fmt.Errorf("%s.sleep_check_sec must be >= 0", label)
	}
	if sleepGap < 0 {
		return fmt.Errorf("%s.sleep_gap_sec must be >= 0", label)
	}
	if networkPoll < 0 {
		return fmt.Errorf("%s.network_poll_sec must be >= 0", label)
	}
	return nil
}

func NormalizeRemoteForwards(cfg *Config) []string {
	if cfg == nil {
		return nil
	}
	out := make([]string, 0, len(cfg.SSH.RemoteForwards))
	seen := make(map[string]struct{})
	add := func(value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		if _, ok := seen[trimmed]; ok {
			return
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	for _, value := range cfg.SSH.RemoteForwards {
		add(value)
	}
	return out
}

func NormalizeLocalForwards(cfg *Config) []string {
	if cfg == nil {
		return nil
	}
	out := make([]string, 0, len(cfg.Client.LocalForwards))
	seen := make(map[string]struct{})
	add := func(value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		if _, ok := seen[trimmed]; ok {
			return
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	for _, value := range cfg.Client.LocalForwards {
		add(value)
	}
	return out
}

func SetRemoteForwards(cfg *Config, forwards []string) {
	if cfg == nil {
		return
	}
	trimmed := make([]string, 0, len(forwards))
	seen := make(map[string]struct{})
	for _, value := range forwards {
		val := strings.TrimSpace(value)
		if val == "" {
			continue
		}
		if _, ok := seen[val]; ok {
			continue
		}
		seen[val] = struct{}{}
		trimmed = append(trimmed, val)
	}
	cfg.SSH.RemoteForwards = append([]string(nil), trimmed...)
}

func SetLocalForwards(cfg *Config, forwards []string) {
	if cfg == nil {
		return
	}
	trimmed := make([]string, 0, len(forwards))
	seen := make(map[string]struct{})
	for _, value := range forwards {
		val := strings.TrimSpace(value)
		if val == "" {
			continue
		}
		if _, ok := seen[val]; ok {
			continue
		}
		seen[val] = struct{}{}
		trimmed = append(trimmed, val)
	}
	cfg.Client.LocalForwards = append([]string(nil), trimmed...)
}

func mergeLocalForwards(single string, list []string) []string {
	out := make([]string, 0, len(list)+1)
	if strings.TrimSpace(single) != "" {
		out = append(out, single)
	}
	out = append(out, list...)
	return out
}

func Save(path string, cfg *Config) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("config path is empty")
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create config dir: %w", err)
		}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func SocketPath(cfg *Config) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	if cfg == nil {
		return "", errors.New("config is nil")
	}
	return filepath.Join(home, ".rpa", "agent.sock"), nil
}

func ClientSocketPath(cfg *Config) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	if cfg == nil {
		return "", errors.New("config is nil")
	}
	return filepath.Join(home, ".rpa", "client.sock"), nil
}

func LogPath(cfg *Config) (string, error) {
	if cfg == nil {
		return "", errors.New("config is nil")
	}
	return expandHome(cfg.Logging.Path)
}

func ClientLogPath(cfg *Config) (string, error) {
	if cfg == nil {
		return "", errors.New("config is nil")
	}
	return expandHome(cfg.ClientLogging.Path)
}

func AgentStatePath(cfg *Config) (string, error) {
	if cfg == nil {
		return "", errors.New("config is nil")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".rpa", "agent.state.json"), nil
}

func ClientStatePath(cfg *Config) (string, error) {
	if cfg == nil {
		return "", errors.New("config is nil")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".rpa", "client.state.json"), nil
}

func expandHome(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is empty")
	}
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
}
