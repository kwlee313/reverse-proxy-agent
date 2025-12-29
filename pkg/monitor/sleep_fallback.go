// Package monitor provides fallback sleep detection helpers using polling.
// It is shared by non-darwin builds and darwin builds without cgo.

package monitor

import (
	"context"
	"time"

	"reverse-proxy-agent/pkg/logging"
)

func sleepWatcher(ctx context.Context, logger *logging.Logger, interval, gap time.Duration, onEvent func(reason string)) {
	if interval <= 0 {
		return
	}
	if gap <= 0 {
		gap = interval * 2
	}
	last := time.Now()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			if now.Sub(last) > gap {
				logger.Info("wake detected (gap=%s)", now.Sub(last).Truncate(time.Second))
				onEvent("wake")
			}
			last = now
		}
	}
}
