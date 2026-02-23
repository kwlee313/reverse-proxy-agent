// Package logging provides a file-backed logger with an in-memory ring buffer for recent lines.
// It is used by the agent runtime and surfaced via IPC log queries.

package logging

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/rs/zerolog"

	"reverse-proxy-agent/pkg/config"
)

type LogBuffer struct {
	mu    sync.Mutex
	size  int
	lines []string
}

func newRingBuffer(size int) *LogBuffer {
	return &LogBuffer{size: size}
}

func NewLogBuffer() *LogBuffer {
	return newRingBuffer(200)
}

func (r *LogBuffer) Add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.lines) >= r.size {
		copy(r.lines, r.lines[1:])
		r.lines[len(r.lines)-1] = line
		return
	}
	r.lines = append(r.lines, line)
}

func (r *LogBuffer) List() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.lines))
	copy(out, r.lines)
	return out
}

type Logger struct {
	path    string
	ring    *LogBuffer
	mu      sync.Mutex
	level   zerolog.Level
	console io.Writer
}

func NewLogger(cfg *config.Config, ring *LogBuffer) (*Logger, error) {
	path, err := config.LogPath(cfg)
	if err != nil {
		return nil, err
	}
	logger, err := NewLoggerWithPath(path, ring)
	if err != nil {
		return nil, err
	}
	logger.SetLevel(cfg.Logging.Level)
	return logger, nil
}

func NewLoggerWithPath(path string, ring *LogBuffer) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	return &Logger{path: path, ring: ring, level: zerolog.InfoLevel}, nil
}

func (l *Logger) Info(format string, args ...any) {
	l.Event("INFO", "message", map[string]any{
		"msg": fmt.Sprintf(format, args...),
	})
}

func (l *Logger) Error(format string, args ...any) {
	l.Event("ERROR", "message", map[string]any{
		"msg": fmt.Sprintf(format, args...),
	})
}

func (l *Logger) Event(level, event string, fields map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()

	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	var buf bytes.Buffer
	writer := zerolog.New(&buf).With().Timestamp().Logger().Level(l.level)
	ev := writer.WithLevel(parseLevel(level)).Str("event", event)
	for k, v := range fields {
		ev = ev.Interface(k, v)
	}
	ev.Send()

	line := strings.TrimSpace(buf.String())
	if line == "" {
		return
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		return
	}
	if l.ring != nil {
		l.ring.Add(line)
	}
	if l.console != nil {
		_, _ = l.console.Write([]byte(line + "\n"))
	}
}

func parseLevel(level string) zerolog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return zerolog.DebugLevel
	case "info":
		return zerolog.InfoLevel
	case "warn", "warning":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	default:
		return zerolog.InfoLevel
	}
}

func (l *Logger) SetLevel(level string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = parseLevel(level)
}

func (l *Logger) SetConsoleWriter(w io.Writer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.console = w
}
