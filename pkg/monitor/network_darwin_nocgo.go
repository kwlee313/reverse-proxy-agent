//go:build darwin && !cgo

// Package monitor provides a polling fallback for network monitoring when cgo is disabled.
// It keeps client/agent restart behavior consistent on macOS builds without cgo.

package monitor

import (
	"context"
	"time"

	"reverse-proxy-agent/pkg/logging"
)

func StartNetworkMonitor(ctx context.Context, cfg Config, logger *logging.Logger, onEvent func(reason string)) {
	if cfg.NetworkPollSec <= 0 {
		return
	}
	if onEvent == nil {
		onEvent = func(string) {}
	}
	logger.Info("network monitor: using polling fallback (cgo disabled)")
	networkWatcher(ctx, logger, time.Duration(cfg.NetworkPollSec)*time.Second, onEvent)
}
