// Package supervisor provides a shared SSH supervision loop for agent and client.
// It centralizes restart policy, monitoring hooks, and observability plumbing.

package supervisor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"reverse-proxy-agent/pkg/logging"
	"reverse-proxy-agent/pkg/monitor"
	"reverse-proxy-agent/pkg/restart"
	"reverse-proxy-agent/pkg/sshutil"
	"reverse-proxy-agent/pkg/state"
	"reverse-proxy-agent/pkg/statefile"
)

type Options struct {
	Kind               string
	Summary            func() string
	MonitorConfig      monitor.Config
	PeriodicRestartSec int
	DebounceMs         int
}

type Runner struct {
	sm *state.StateMachine

	mu     sync.Mutex
	cmd    *exec.Cmd
	logger *logging.Logger

	stopCh   chan struct{}
	stopOnce sync.Once

	restartCount int
	lastExit     string

	policy  restart.Policy
	backoff *restart.Backoff

	errLines *sshutil.LineBuffer

	lastSuccess time.Time
	lastClass   string
	lastTrigger time.Time

	startSuccessCount int
	startFailureCount int
	exitSuccessCount  int
	exitFailureCount  int
	lastTriggerReason string

	stateWriter func(statefile.Snapshot)
}

const successGracePeriod = 2 * time.Second

func New(policy restart.Policy, backoff *restart.Backoff) *Runner {
	return &Runner{
		sm:      state.NewStateMachine(),
		stopCh:  make(chan struct{}),
		policy:  policy,
		backoff: backoff,
	}
}

func (r *Runner) Start(build func() (*exec.Cmd, error)) error {
	if err := r.sm.Transition(state.StateConnecting); err != nil {
		return err
	}

	cmd, err := build()
	if err != nil {
		_ = r.sm.Transition(state.StateStopped)
		r.recordStartFailure()
		return err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = r.sm.Transition(state.StateStopped)
		r.recordStartFailure()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = r.sm.Transition(state.StateStopped)
		r.recordStartFailure()
		return err
	}

	if err := cmd.Start(); err != nil {
		_ = r.sm.Transition(state.StateStopped)
		r.recordStartFailure()
		return err
	}

	r.mu.Lock()
	r.cmd = cmd
	r.errLines = sshutil.NewLineBuffer(10)
	r.mu.Unlock()

	go drain(stdout, nil)
	go drain(stderr, r.errLines)

	if err := r.sm.Transition(state.StateConnected); err != nil {
		return err
	}
	r.recordStartSuccess()
	r.scheduleSuccessMark(cmd)
	return nil
}

func (r *Runner) Stop() error {
	r.mu.Lock()
	cmd := r.cmd
	r.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() {
			done <- cmd.Wait()
		}()
		select {
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
		case <-done:
		}
	}
	if err := r.sm.Transition(state.StateStopped); err != nil {
		return err
	}
	return nil
}

func (r *Runner) RunWithLogger(logger *logging.Logger, build func() (*exec.Cmd, error), opts Options) error {
	startEvent := opts.Kind + "_start"
	stopEvent := opts.Kind + "_stop"
	stopRequestedEvent := opts.Kind + "_stop_requested"
	if opts.Kind == "" {
		startEvent = "start"
		stopEvent = "stop"
		stopRequestedEvent = "stop_requested"
	}

	logger.Event("INFO", startEvent, map[string]any{
		"summary": opts.Summary(),
	})
	defer logger.Event("INFO", stopEvent, nil)

	r.setLogger(logger)
	defer r.setLogger(nil)

	monitorCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var eventWG sync.WaitGroup
	eventWG.Add(1)
	go func() {
		defer eventWG.Done()
		monitor.StartSleepMonitor(monitorCtx, opts.MonitorConfig, logger, func(reason string) {
			r.triggerRestart(logger, reason, opts.DebounceMs)
		})
	}()
	eventWG.Add(1)
	go func() {
		defer eventWG.Done()
		monitor.StartNetworkMonitor(monitorCtx, opts.MonitorConfig, logger, func(reason string) {
			r.triggerRestart(logger, reason, opts.DebounceMs)
		})
	}()

	var periodicStop chan struct{}
	if opts.PeriodicRestartSec > 0 {
		periodicStop = make(chan struct{})
		go r.periodicRestartLoop(logger, time.Duration(opts.PeriodicRestartSec)*time.Second, opts.DebounceMs, periodicStop)
	}
	defer func() {
		cancel()
		eventWG.Wait()
		if periodicStop != nil {
			close(periodicStop)
		}
	}()

	go func() {
		<-r.stopCh
		cancel()
	}()

	for {
		select {
		case <-r.stopCh:
			logger.Event("INFO", stopRequestedEvent, nil)
			return r.Stop()
		default:
		}

		if err := r.Start(build); err != nil {
			r.recordExit(fmt.Sprintf("start failed: %v", err))
			r.setLastTriggerReason("start failed")
			logger.Event("ERROR", "ssh_start_failed", map[string]any{
				"error": err.Error(),
			})
			r.mu.Lock()
			r.restartCount++
			r.mu.Unlock()
			if err := r.sleepWithBackoff(logger); err != nil {
				return err
			}
			continue
		}

		logger.Event("INFO", "ssh_started", map[string]any{
			"summary": opts.Summary(),
		})
		r.mu.Lock()
		cmd := r.cmd
		r.mu.Unlock()
		if cmd == nil {
			r.recordExit("ssh command not started")
			logger.Event("ERROR", "ssh_start_failed", map[string]any{
				"error": "ssh command not started",
			})
			_ = r.sm.Transition(state.StateStopped)
			time.Sleep(2 * time.Second)
			continue
		}

		err := cmd.Wait()
		exitCode := 0
		if err != nil {
			r.recordExitFailure()
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		} else {
			r.recordExitSuccess()
		}
		class := sshutil.ClassifyExit(r.errLines, exitCode, err)
		r.setLastClass(class)
		exitMsg := sshutil.FormatExit(exitCode, err)
		if class != "clean" {
			exitMsg = fmt.Sprintf("%s (%s)", exitMsg, class)
		}
		r.recordExit(exitMsg)
		if err != nil {
			if summary := stderrSummary(r.errLines); summary != "" {
				logger.Event("ERROR", "ssh_exited", map[string]any{
					"exit":   exitMsg,
					"class":  class,
					"stderr": summary,
				})
			} else {
				logger.Event("ERROR", "ssh_exited", map[string]any{
					"exit":  exitMsg,
					"class": class,
				})
			}
		} else {
			logger.Event("INFO", "ssh_exited", map[string]any{
				"exit":  exitMsg,
				"class": class,
			})
		}

		_ = r.sm.Transition(state.StateStopped)

		r.mu.Lock()
		r.cmd = nil
		r.mu.Unlock()

		if !r.shouldRestart(exitCode, err, class) {
			logger.Event("INFO", "restart_policy_stop", map[string]any{
				"policy": r.policy.Name(),
				"class":  class,
			})
			return nil
		}
		if class == "auth" || class == "hostkey" {
			logger.Event("ERROR", "restart_policy_stop", map[string]any{
				"policy": r.policy.Name(),
				"class":  class,
				"reason": "manual intervention required",
			})
			return nil
		}
		if err == nil {
			r.backoff.Reset()
		}
		r.mu.Lock()
		r.restartCount++
		r.mu.Unlock()

		if err := r.sleepWithBackoff(logger); err != nil {
			return err
		}
	}
}

func (r *Runner) shouldRestart(exitCode int, err error, class string) bool {
	if class == "auth" || class == "hostkey" {
		return false
	}
	switch r.policy {
	case restart.PolicyOnFailure:
		return err != nil || exitCode != 0
	default:
		return true
	}
}

func (r *Runner) sleepWithBackoff(logger *logging.Logger) error {
	delay := r.backoff.Next()
	if delay <= 0 {
		return nil
	}
	logger.Event("INFO", "restart_scheduled", map[string]any{
		"delay_ms": delay.Round(time.Millisecond).Milliseconds(),
	})
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-r.stopCh:
		logger.Event("INFO", "stop_during_backoff", nil)
		return r.Stop()
	case <-timer.C:
		return nil
	}
}

func (r *Runner) RequestStop() {
	r.stopOnce.Do(func() {
		close(r.stopCh)
		go func() {
			_ = r.Stop()
		}()
	})
}

func (r *Runner) RequestRestart(reason string, debounceMs int) {
	r.mu.Lock()
	logger := r.logger
	r.mu.Unlock()
	r.triggerRestart(logger, reason, debounceMs)
}

func (r *Runner) triggerRestart(logger *logging.Logger, reason string, debounceMs int) {
	if r.State() != state.StateConnected {
		return
	}
	r.setLastTriggerReason(reason)
	if !r.allowTrigger(time.Duration(debounceMs) * time.Millisecond) {
		if logger != nil {
			logger.Event("INFO", "restart_skipped", map[string]any{
				"reason": reason,
				"detail": "debounced",
			})
		}
		return
	}
	if logger != nil {
		logger.Event("INFO", "restart_triggered", map[string]any{
			"reason": reason,
		})
	}
	r.terminateProcess()
}

func (r *Runner) periodicRestartLoop(logger *logging.Logger, interval time.Duration, debounceMs int, stop <-chan struct{}) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-r.stopCh:
			return
		case <-ticker.C:
			if r.State() != state.StateConnected {
				continue
			}
			if !r.allowTrigger(time.Duration(debounceMs) * time.Millisecond) {
				logger.Event("INFO", "restart_skipped", map[string]any{
					"reason": "periodic",
					"detail": "debounced",
				})
				continue
			}
			r.setLastTriggerReason("periodic")
			logger.Event("INFO", "restart_triggered", map[string]any{
				"reason": "periodic",
			})
			r.terminateProcess()
		}
	}
}

func (r *Runner) allowTrigger(window time.Duration) bool {
	if window <= 0 {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if !r.lastTrigger.IsZero() && now.Sub(r.lastTrigger) < window {
		return false
	}
	r.lastTrigger = now
	return true
}

func (r *Runner) terminateProcess() {
	r.mu.Lock()
	cmd := r.cmd
	r.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
}

func (r *Runner) State() state.State {
	return r.sm.State()
}

func (r *Runner) RestartCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.restartCount
}

func (r *Runner) LastExitReason() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastExit
}

func (r *Runner) LastSuccess() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastSuccess
}

func (r *Runner) LastClass() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastClass
}

func (r *Runner) LastTriggerReason() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastTriggerReason
}

func (r *Runner) StartSuccessCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.startSuccessCount
}

func (r *Runner) StartFailureCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.startFailureCount
}

func (r *Runner) ExitSuccessCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.exitSuccessCount
}

func (r *Runner) ExitFailureCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.exitFailureCount
}

func (r *Runner) CurrentBackoff() time.Duration {
	return r.backoff.Current()
}

func (r *Runner) recordExit(reason string) {
	r.mu.Lock()
	r.lastExit = reason
	writer := r.stateWriter
	snap := r.snapshotLocked()
	r.mu.Unlock()
	r.writeSnapshot(writer, snap)
}

func (r *Runner) scheduleSuccessMark(cmd *exec.Cmd) {
	go func() {
		time.Sleep(successGracePeriod)
		r.mu.Lock()
		if r.cmd != cmd || r.sm.State() != state.StateConnected {
			r.mu.Unlock()
			return
		}
		r.lastSuccess = time.Now()
		writer := r.stateWriter
		snap := r.snapshotLocked()
		r.mu.Unlock()
		r.writeSnapshot(writer, snap)
	}()
}

func (r *Runner) setLastClass(class string) {
	r.mu.Lock()
	r.lastClass = class
	writer := r.stateWriter
	snap := r.snapshotLocked()
	r.mu.Unlock()
	r.writeSnapshot(writer, snap)
}

func (r *Runner) setLastTriggerReason(reason string) {
	r.mu.Lock()
	r.lastTriggerReason = reason
	writer := r.stateWriter
	snap := r.snapshotLocked()
	r.mu.Unlock()
	r.writeSnapshot(writer, snap)
}

func (r *Runner) SetStateWriter(writer func(statefile.Snapshot)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stateWriter = writer
}

func (r *Runner) snapshotLocked() statefile.Snapshot {
	snap := statefile.Snapshot{
		LastExit:    r.lastExit,
		LastClass:   r.lastClass,
		LastTrigger: r.lastTriggerReason,
	}
	if !r.lastSuccess.IsZero() {
		snap.LastSuccessUnix = r.lastSuccess.Unix()
	}
	return snap
}

func (r *Runner) writeSnapshot(writer func(statefile.Snapshot), snap statefile.Snapshot) {
	if writer == nil {
		return
	}
	writer(snap)
}

func (r *Runner) recordStartSuccess() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startSuccessCount++
}

func (r *Runner) recordStartFailure() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startFailureCount++
}

func (r *Runner) recordExitSuccess() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.exitSuccessCount++
}

func (r *Runner) recordExitFailure() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.exitFailureCount++
}

func (r *Runner) setLogger(logger *logging.Logger) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logger = logger
}

func drain(r io.Reader, lines *sshutil.LineBuffer) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if lines != nil {
			lines.Add(scanner.Text())
		}
	}
}

func stderrSummary(lines *sshutil.LineBuffer) string {
	if lines == nil {
		return ""
	}
	list := lines.Lines()
	if len(list) == 0 {
		return ""
	}
	start := len(list) - 2
	if start < 0 {
		start = 0
	}
	summary := strings.Join(list[start:], " | ")
	if len(summary) > 200 {
		return summary[:200]
	}
	return summary
}
