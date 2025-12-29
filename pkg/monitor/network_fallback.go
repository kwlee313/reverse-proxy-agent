// Package monitor provides a polling fallback for network change detection.
// It is shared by non-darwin builds and darwin builds without cgo.

package monitor

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"reverse-proxy-agent/pkg/logging"
)

func networkWatcher(ctx context.Context, logger *logging.Logger, interval time.Duration, onEvent func(reason string)) {
	if interval <= 0 {
		return
	}
	prev, _ := networkFingerprint()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			next, err := networkFingerprint()
			if err != nil {
				logger.Error("network fingerprint failed: %v", err)
				continue
			}
			if next != prev {
				logger.Info("network change detected")
				onEvent("network change")
				prev = next
			}
		}
	}
}

func networkFingerprint() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	entries := make([]string, 0, len(ifaces))
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			entries = append(entries, fmt.Sprintf("%s|%s", iface.Name, addr.String()))
		}
	}
	sort.Strings(entries)
	return strings.Join(entries, ","), nil
}
