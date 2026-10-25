package inspect

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/common/model"

	"yunwei/internal/config"
	"yunwei/internal/discovery"
)

// rebootLookback is the window over which reboots are counted.
const rebootLookback = 24 * time.Hour

// rebootCounts counts boot-time changes per host within a bounded window. It
// runs only when the recent_reboot item is configured, so the most-reboots
// ranking is by frequency rather than by how recent the last boot was.
func rebootCounts(ctx context.Context, src Source, items map[string]config.Item,
	hosts map[string]*discovery.Host, opts Options) (counts map[string]float64, queried bool) {

	if _, ok := items[checkReboot]; !ok {
		return nil, false
	}
	window := rebootLookback
	if window > opts.MaxRange {
		window = opts.MaxRange
	}
	q := fmt.Sprintf("changes(node_boot_time_seconds[%ds])", int(window.Seconds()))
	val, _, err := src.Query(ctx, q, opts.Now)
	if err != nil {
		return nil, true
	}
	vec, ok := val.(model.Vector)
	if !ok {
		return nil, true
	}
	counts = map[string]float64{}
	for _, s := range vec {
		v := float64(s.Value)
		if !usable(v) {
			continue
		}
		h := hosts[discovery.HostKey(string(s.Metric["instance"]))]
		if h == nil {
			continue
		}
		if v > counts[h.Key] {
			counts[h.Key] = v
		}
	}
	return counts, true
}
