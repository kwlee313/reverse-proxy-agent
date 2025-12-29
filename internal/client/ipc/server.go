// Package ipc provides a Unix socket server that exposes client status, metrics, and logs.
// It is started by the client run path in cli.

package ipc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"reverse-proxy-agent/internal/client"
	"reverse-proxy-agent/pkg/config"
	"reverse-proxy-agent/pkg/logging"
)

type Server struct {
	socketPath string
	client     *client.Client
	logs       *logging.LogBuffer
	startedAt  time.Time

	mu       sync.Mutex
	listener net.Listener
}

type request struct {
	Command string            `json:"command"`
	Args    map[string]string `json:"args,omitempty"`
}

type response struct {
	OK      bool              `json:"ok"`
	Message string            `json:"message,omitempty"`
	Data    map[string]string `json:"data,omitempty"`
	Logs    []string          `json:"logs,omitempty"`
}

func NewServer(cfg *config.Config, clientInstance *client.Client, logs *logging.LogBuffer) (*Server, error) {
	socketPath, err := config.ClientSocketPath(cfg)
	if err != nil {
		return nil, err
	}
	return &Server{
		socketPath: socketPath,
		client:     clientInstance,
		logs:       logs,
		startedAt:  time.Now(),
	}, nil
}

func (s *Server) Start() error {
	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0o755); err != nil {
		return fmt.Errorf("create socket dir: %w", err)
	}
	_ = os.Remove(s.socketPath)
	lis, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen on socket: %w", err)
	}
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		_ = lis.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}

	s.mu.Lock()
	s.listener = lis
	s.mu.Unlock()

	go s.acceptLoop()
	return nil
}

func (s *Server) Stop() {
	s.mu.Lock()
	lis := s.listener
	s.listener = nil
	s.mu.Unlock()
	if lis != nil {
		_ = lis.Close()
	}
	_ = os.Remove(s.socketPath)
}

func (s *Server) acceptLoop() {
	for {
		s.mu.Lock()
		lis := s.listener
		s.mu.Unlock()
		if lis == nil {
			return
		}
		conn, err := lis.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return
	}
	var req request
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		writeResponse(conn, response{OK: false, Message: "invalid request"})
		return
	}

	switch req.Command {
	case "status":
		s.handleStatus(conn)
	case "metrics":
		s.handleMetrics(conn)
	case "logs":
		s.handleLogs(conn)
	case "stop":
		s.handleStop(conn)
	case "add_local_forward":
		s.handleAddLocalForward(conn, req.Args)
	case "remove_local_forward":
		s.handleRemoveLocalForward(conn, req.Args)
	case "clear_local_forwards":
		s.handleClearLocalForwards(conn)
	default:
		writeResponse(conn, response{OK: false, Message: "unknown command"})
	}
}

func (s *Server) handleStatus(conn net.Conn) {
	state := s.client.State().String()
	data := map[string]string{
		"state":        state,
		"summary":      s.client.ConfigSummary(),
		"uptime":       time.Since(s.startedAt).Truncate(time.Second).String(),
		"socket":       s.socketPath,
		"restarts":     fmt.Sprintf("%d", s.client.RestartCount()),
		"last_exit":    s.client.LastExitReason(),
		"last_class":   s.client.LastClass(),
		"last_trigger": s.client.LastTriggerReason(),
	}
	if !s.client.LastSuccess().IsZero() {
		data["last_success_unix"] = fmt.Sprintf("%d", s.client.LastSuccess().Unix())
	}
	if backoff := s.client.CurrentBackoff(); backoff > 0 {
		data["backoff_ms"] = fmt.Sprintf("%d", backoff.Milliseconds())
	}
	writeResponse(conn, response{OK: true, Data: data})
}

func (s *Server) handleMetrics(conn net.Conn) {
	data := map[string]string{
		"rpa_client_state":               fmt.Sprintf("%d", s.client.State()),
		"rpa_client_restart_total":       fmt.Sprintf("%d", s.client.RestartCount()),
		"rpa_client_uptime_sec":          fmt.Sprintf("%d", int(time.Since(s.startedAt).Seconds())),
		"rpa_client_start_success_total": fmt.Sprintf("%d", s.client.StartSuccessCount()),
		"rpa_client_start_failure_total": fmt.Sprintf("%d", s.client.StartFailureCount()),
		"rpa_client_exit_success_total":  fmt.Sprintf("%d", s.client.ExitSuccessCount()),
		"rpa_client_exit_failure_total":  fmt.Sprintf("%d", s.client.ExitFailureCount()),
		"rpa_client_last_trigger":        s.client.LastTriggerReason(),
	}
	if !s.client.LastSuccess().IsZero() {
		data["rpa_client_last_success_unix"] = fmt.Sprintf("%d", s.client.LastSuccess().Unix())
	}
	if backoff := s.client.CurrentBackoff(); backoff > 0 {
		data["rpa_client_backoff_ms"] = fmt.Sprintf("%d", backoff.Milliseconds())
	}
	writeResponse(conn, response{OK: true, Data: data})
}

func (s *Server) handleLogs(conn net.Conn) {
	writeResponse(conn, response{OK: true, Logs: s.logs.List()})
}

func (s *Server) handleStop(conn net.Conn) {
	writeResponse(conn, response{OK: true, Message: "stopping"})
	go s.client.RequestStop()
}

func (s *Server) handleAddLocalForward(conn net.Conn, args map[string]string) {
	forward := ""
	if args != nil {
		forward = args["local_forward"]
	}
	forward = strings.TrimSpace(forward)
	if forward == "" {
		writeResponse(conn, response{OK: false, Message: "local_forward is required"})
		return
	}
	added := s.client.EnsureLocalForward(forward)
	msg := "local forward already present"
	if added {
		msg = "local forward added"
		s.client.RequestRestart("client_add")
	}
	writeResponse(conn, response{
		OK:      true,
		Message: msg,
		Data:    map[string]string{"added": fmt.Sprintf("%t", added)},
	})
}

func (s *Server) handleRemoveLocalForward(conn net.Conn, args map[string]string) {
	forward := ""
	if args != nil {
		forward = args["local_forward"]
	}
	removed, err := s.client.RemoveLocalForward(forward)
	if err != nil {
		writeResponse(conn, response{OK: false, Message: err.Error()})
		return
	}
	msg := "local forward not found"
	if removed {
		msg = "local forward removed"
	}
	writeResponse(conn, response{
		OK:      true,
		Message: msg,
		Data:    map[string]string{"removed": fmt.Sprintf("%t", removed)},
	})
}

func (s *Server) handleClearLocalForwards(conn net.Conn) {
	cleared := s.client.ClearLocalForwards()
	msg := "no local forwards to clear"
	if cleared {
		msg = "local forwards cleared; stopping client"
	}
	writeResponse(conn, response{
		OK:      true,
		Message: msg,
		Data:    map[string]string{"cleared": fmt.Sprintf("%t", cleared)},
	})
}

func writeResponse(conn net.Conn, resp response) {
	enc := json.NewEncoder(conn)
	_ = enc.Encode(resp)
}
