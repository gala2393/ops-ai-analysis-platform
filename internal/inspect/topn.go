package inspect

import (
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
	hosts map[string]*discovery.Host, findings []*Finding, opts Options) []TopList {

	build := func(id, listID, title string, desc, onlyAbnormal bool) TopList {
		l := TopList{ID: listID, Title: title, Unit: items[id].Unit, Entries: []TopEntry{}}
		for hk, hv := range values[id] {
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

	return []TopList{
		build(checkDisk, TopDiskUsage, "磁盘使用率最高", true, false),
		build(checkGrowth, TopDiskFill, "磁盘增长最快（预计写满天数最少）", false, false),
		build(checkReboot, TopReboot, "最近重启（距开机时间最短）", false, true),
		repeat,
	}
}
