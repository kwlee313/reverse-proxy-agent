//go:build darwin && !cgo

// Package monitor provides a polling fallback for sleep detection when cgo is disabled.
// It keeps client/agent restart behavior consistent on macOS builds without cgo.

package monitor

import (
	"context"
	"time"

	"reverse-proxy-agent/pkg/logging"
)

func StartSleepMonitor(ctx context.Context, cfg Config, logger *logging.Logger, onEvent func(reason string)) {
	if cfg.SleepCheckSec <= 0 {
		return
	}
	if onEvent == nil {
		onEvent = func(string) {}
	}
	logger.Info("sleep monitor: using polling fallback (cgo disabled)")
	sleepWatcher(ctx, logger, time.Duration(cfg.SleepCheckSec)*time.Second, time.Duration(cfg.SleepGapSec)*time.Second, onEvent)
}
