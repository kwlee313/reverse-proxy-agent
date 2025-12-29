// Package ipc provides a Unix socket server that exposes agent status, metrics, and logs.
// It is used by the agent run path in cli and is not required by clients.

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

	"reverse-proxy-agent/internal/agent"
	"reverse-proxy-agent/pkg/config"
	"reverse-proxy-agent/pkg/logging"
)

type Server struct {
	socketPath string
	agent      *agent.Agent
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

func NewServer(cfg *config.Config, agentInstance *agent.Agent, logs *logging.LogBuffer) (*Server, error) {
	socketPath, err := config.SocketPath(cfg)
	if err != nil {
		return nil, err
	}
	return &Server{
		socketPath: socketPath,
		agent:      agentInstance,
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
	case "add_forward":
		s.handleAddForward(conn, req.Args)
	case "remove_forward":
		s.handleRemoveForward(conn, req.Args)
	case "clear_forwards":
		s.handleClearForwards(conn)
	default:
		writeResponse(conn, response{OK: false, Message: "unknown command"})
	}
}

func (s *Server) handleStatus(conn net.Conn) {
	state := s.agent.State().String()
	data := map[string]string{
		"state":        state,
		"summary":      s.agent.ConfigSummary(),
		"uptime":       time.Since(s.startedAt).Truncate(time.Second).String(),
		"socket":       s.socketPath,
		"restarts":     fmt.Sprintf("%d", s.agent.RestartCount()),
		"last_exit":    s.agent.LastExitReason(),
		"last_class":   s.agent.LastClass(),
		"last_trigger": s.agent.LastTriggerReason(),
	}
	if forwards := s.agent.RemoteForwards(); len(forwards) > 0 {
		data["remote_forwards"] = strings.Join(forwards, ",")
	}
	if !s.agent.LastSuccess().IsZero() {
		data["last_success_unix"] = fmt.Sprintf("%d", s.agent.LastSuccess().Unix())
	}
	if backoff := s.agent.CurrentBackoff(); backoff > 0 {
		data["backoff_ms"] = fmt.Sprintf("%d", backoff.Milliseconds())
	}
	writeResponse(conn, response{OK: true, Data: data})
}

func (s *Server) handleMetrics(conn net.Conn) {
	data := map[string]string{
		"rpa_agent_state":               fmt.Sprintf("%d", s.agent.State()),
		"rpa_agent_restart_total":       fmt.Sprintf("%d", s.agent.RestartCount()),
		"rpa_agent_uptime_sec":          fmt.Sprintf("%d", int(time.Since(s.startedAt).Seconds())),
		"rpa_agent_start_success_total": fmt.Sprintf("%d", s.agent.StartSuccessCount()),
		"rpa_agent_start_failure_total": fmt.Sprintf("%d", s.agent.StartFailureCount()),
		"rpa_agent_exit_success_total":  fmt.Sprintf("%d", s.agent.ExitSuccessCount()),
		"rpa_agent_exit_failure_total":  fmt.Sprintf("%d", s.agent.ExitFailureCount()),
		"rpa_agent_last_trigger":        s.agent.LastTriggerReason(),
	}
	if !s.agent.LastSuccess().IsZero() {
		data["rpa_agent_last_success_unix"] = fmt.Sprintf("%d", s.agent.LastSuccess().Unix())
	}
	if backoff := s.agent.CurrentBackoff(); backoff > 0 {
		data["rpa_agent_backoff_ms"] = fmt.Sprintf("%d", backoff.Milliseconds())
	}
	writeResponse(conn, response{OK: true, Data: data})
}

func (s *Server) handleLogs(conn net.Conn) {
	writeResponse(conn, response{OK: true, Logs: s.logs.List()})
}

func (s *Server) handleStop(conn net.Conn) {
	writeResponse(conn, response{OK: true, Message: "stopping"})
	go s.agent.RequestStop()
}

func (s *Server) handleAddForward(conn net.Conn, args map[string]string) {
	forward := ""
	if args != nil {
		forward = args["remote_forward"]
	}
	added, err := s.agent.AddRemoteForward(forward)
	if err != nil {
		writeResponse(conn, response{OK: false, Message: err.Error()})
		return
	}
	msg := "remote forward already present"
	if added {
		msg = "remote forward added"
	}
	writeResponse(conn, response{
		OK:      true,
		Message: msg,
		Data:    map[string]string{"added": fmt.Sprintf("%t", added)},
	})
}

func (s *Server) handleRemoveForward(conn net.Conn, args map[string]string) {
	forward := ""
	if args != nil {
		forward = args["remote_forward"]
	}
	removed, err := s.agent.RemoveRemoteForward(forward)
	if err != nil {
		writeResponse(conn, response{OK: false, Message: err.Error()})
		return
	}
	msg := "remote forward not found"
	if removed {
		msg = "remote forward removed"
	}
	writeResponse(conn, response{
		OK:      true,
		Message: msg,
		Data:    map[string]string{"removed": fmt.Sprintf("%t", removed)},
	})
}

func (s *Server) handleClearForwards(conn net.Conn) {
	cleared := s.agent.ClearRemoteForwards()
	msg := "no remote forwards to clear"
	if cleared {
		msg = "remote forwards cleared; stopping agent"
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
