// Package agent provides a Unix socket client for querying a running agent.
// It is used by cli commands such as status, logs, and metrics.

package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"

	"reverse-proxy-agent/pkg/config"
)

type Response struct {
	OK      bool              `json:"ok"`
	Message string            `json:"message,omitempty"`
	Data    map[string]string `json:"data,omitempty"`
	Logs    []string          `json:"logs,omitempty"`
}

var (
	ErrAgentNotRunning    = errors.New("agent not running")
	ErrAgentSocketRefused = errors.New("agent socket refused")
)

func Query(cfg *config.Config, command string) (*Response, error) {
	return send(cfg, command, nil)
}

func AddRemoteForward(cfg *config.Config, forward string) (*Response, error) {
	return send(cfg, "add_forward", map[string]string{
		"remote_forward": forward,
	})
}

func RemoveRemoteForward(cfg *config.Config, forward string) (*Response, error) {
	return send(cfg, "remove_forward", map[string]string{
		"remote_forward": forward,
	})
}

func ClearRemoteForwards(cfg *config.Config) (*Response, error) {
	return send(cfg, "clear_forwards", nil)
}

func send(cfg *config.Config, command string, args map[string]string) (*Response, error) {
	socketPath, err := config.SocketPath(cfg)
	if err != nil {
		return nil, err
	}
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, friendlyDialError("agent", err)
	}
	defer conn.Close()

	req := struct {
		Command string            `json:"command"`
		Args    map[string]string `json:"args,omitempty"`
	}{
		Command: command,
		Args:    args,
	}
	enc := json.NewEncoder(conn)
	if err := enc.Encode(req); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	reader := bufio.NewReader(conn)
	var resp Response
	if err := json.NewDecoder(reader).Decode(&resp); err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return &resp, nil
}

func friendlyDialError(kind string, err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ENOENT):
		return fmt.Errorf("%w: %s not running; start with `rpa %s up` or `rpa %s run`", ErrAgentNotRunning, kind, kind, kind)
	case errors.Is(err, syscall.ECONNREFUSED):
		return fmt.Errorf("%w: %s socket refused connection; check if %s is running", ErrAgentSocketRefused, kind, kind)
	default:
		return fmt.Errorf("connect to %s: %w", kind, err)
	}
}
