package inspect

import (
	"math"
	"sort"

	"yunwei/internal/config"
	"yunwei/internal/discovery"
)

// Check ids the rankings are built from.
const (
	checkDisk    = "disk_usage"
	checkGrowth  = "disk_days_to_full"
	checkReboot  = "recent_reboot"
	TopDiskUsage = "top_disk_usage"
	TopDiskFill  = "top_disk_growth"
	TopReboot    = "top_reboot"
	TopRepeat    = "top_repeat_hosts"
)

// buildTop builds the four Top N rankings. Lists whose source item is not
// configured are present but empty.
func buildTop(values map[string]map[string]*hostValue, items map[string]config.Item,
	hosts map[string]*discovery.Host, findings []*Finding, reboots map[string]float64, opts Options) []TopList {

	build := func(id, listID, title string, desc, onlyAbnormal bool) TopList {
		l := TopList{ID: listID, Title: title, Unit: items[id].Unit, Entries: []TopEntry{}}
		for _, hv := range values[id] {
			hk := hv.hostKey
			h := hosts[hk]
			if h == nil || (onlyAbnormal && hv.level == LevelOK) {
				continue
			}
			e := TopEntry{HostKey: hk, Host: h.Name, Value: hv.value}
			if mp := hv.labels["mountpoint"]; mp != "" {
				e.Extra = map[string]string{"mountpoint": mp}
			}
			l.Entries = append(l.Entries, e)
		}
		sort.Slice(l.Entries, func(i, j int) bool {
			a, b := l.Entries[i], l.Entries[j]
			if a.Value != b.Value {
				return (a.Value > b.Value) == desc
			}
			return a.Host < b.Host
		})
		if len(l.Entries) > opts.TopN {
			l.Entries = l.Entries[:opts.TopN]
		}
		return l
	}

	repeat := TopList{ID: TopRepeat, Title: "重复出问题主机", Unit: "次", Entries: []TopEntry{}}
	seen := map[string]bool{}
	for _, f := range findings {
		if seen[f.HostKey] {
			continue
		}
		seen[f.HostKey] = true
		if n := opts.History[f.HostKey] + 1; n >= 2 {
			repeat.Entries = append(repeat.Entries, TopEntry{HostKey: f.HostKey, Host: f.Host, Value: float64(n)})
		}
	}
	sort.Slice(repeat.Entries, func(i, j int) bool {
		a, b := repeat.Entries[i], repeat.Entries[j]
		if a.Value != b.Value {
			return a.Value > b.Value
		}
		return a.Host < b.Host
	})
	if len(repeat.Entries) > opts.TopN {
		repeat.Entries = repeat.Entries[:opts.TopN]
	}

	reboot := TopList{ID: TopReboot, Title: "重启最多（近 24 小时重启次数）", Unit: "次", Entries: []TopEntry{}}
	// A host the recent_reboot item flags has rebooted at least once, even
	// when the sampled window missed the boot-time step.
	counted := map[string]float64{}
	for hk, n := range reboots {
		counted[hk] = n
	}
	for _, hv := range values[checkReboot] {
		if hv.level != LevelOK && counted[hv.hostKey] < 1 {
			counted[hv.hostKey] = 1
		}
	}
	for hk, n := range counted {
		if h := hosts[hk]; h != nil && n > 0 {
			reboot.Entries = append(reboot.Entries, TopEntry{HostKey: hk, Host: h.Name, Value: n})
		}
	}
	// Equal counts fall back to the most recent boot (smallest uptime).
	uptime := func(e TopEntry) float64 {
		if hv := values[checkReboot][e.HostKey]; hv != nil {
			return hv.value
		}
		return math.Inf(1)
	}
	sort.Slice(reboot.Entries, func(i, j int) bool {
		a, b := reboot.Entries[i], reboot.Entries[j]
		if a.Value != b.Value {
			return a.Value > b.Value
		}
		if ua, ub := uptime(a), uptime(b); ua != ub {
			return ua < ub
		}
		return a.Host < b.Host
	})
	if len(reboot.Entries) > opts.TopN {
		reboot.Entries = reboot.Entries[:opts.TopN]
	}

	return []TopList{
		build(checkDisk, TopDiskUsage, "磁盘使用率最高", true, false),
		build(checkGrowth, TopDiskFill, "磁盘增长最快（预计写满天数最少）", false, false),
		reboot,
		repeat,
	}
}
