// Package client provides a Unix socket client for querying a running client.
// It is used by cli client commands such as status, logs, and metrics.

package client

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"

	"reverse-proxy-agent/pkg/config"
)

type Response struct {
	OK      bool              `json:"ok"`
	Message string            `json:"message,omitempty"`
	Data    map[string]string `json:"data,omitempty"`
	Logs    []string          `json:"logs,omitempty"`
}

type request struct {
	Command string            `json:"command"`
	Args    map[string]string `json:"args,omitempty"`
}

func Query(cfg *config.Config, command string) (*Response, error) {
	return send(cfg, request{Command: command})
}

func AddLocalForward(cfg *config.Config, forward string) (*Response, error) {
	return send(cfg, request{
		Command: "add_local_forward",
		Args:    map[string]string{"local_forward": forward},
	})
}

func RemoveLocalForward(cfg *config.Config, forward string) (*Response, error) {
	return send(cfg, request{
		Command: "remove_local_forward",
		Args:    map[string]string{"local_forward": forward},
	})
}

func ClearLocalForwards(cfg *config.Config) (*Response, error) {
	return send(cfg, request{Command: "clear_local_forwards"})
}

func send(cfg *config.Config, req request) (*Response, error) {
	socketPath, err := config.ClientSocketPath(cfg)
	if err != nil {
		return nil, err
	}
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect to client: %w", err)
	}
	defer conn.Close()

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
