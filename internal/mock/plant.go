package mock

import (
	"fmt"
	"math/rand"
	"sort"
	"time"
)

// Check ids and levels used in the manifest; they match config/inspection.yaml.
const (
	checkDisk       = "disk_usage"
	checkDaysToFull = "disk_days_to_full"
	checkCPU        = "cpu_usage"
	checkCPUPeak    = "cpu_peak"
	checkMem        = "memory_usage"
	checkSwap       = "swap_usage"
	checkInode      = "inode_usage"
	checkReboot     = "recent_reboot"
	checkOOM        = "oom_kills"
	checkMySQLConn  = "mysql_connections"
	checkTargetDown = "target_down"

	levelWarn = "warn"
	levelCrit = "crit"
)

func (f *Fleet) groupRole() string {
	if r := f.Scenario.Anomalies.DiskGroupRole; r != "" {
		return r
	}
	return "app"
}

func (f *Fleet) groupDC() string {
	if d := f.Scenario.Anomalies.DiskGroupDC; d != "" {
		return d
	}
	return "sh1"
}

// diskGroupIndexes picks which host slots belong to the full-disk group. They
// are chosen before labels are drawn so the group can be given its shared
// role/dc up front.
func (f *Fleet) diskGroupIndexes(rng *rand.Rand) []int {
	n := f.Scenario.Anomalies.DiskGroup
	if n > f.Scenario.Hosts {
		n = f.Scenario.Hosts
	}
	if n <= 0 {
		return nil
	}
	idx := rng.Perm(f.Scenario.Hosts)[:n]
	sort.Ints(idx)
	return idx
}

func (f *Fleet) record(check string, h *Host, level string) {
	f.Planted = append(f.Planted, Anomaly{CheckID: check, Host: h.Name, Level: level})
	f.planted[h] = true
}

// take selects n not-yet-planted hosts matching pred, in seeded random order.
func (f *Fleet) take(rng *rand.Rand, n int, pred func(*Host) bool) []*Host {
	var out []*Host
	for _, idx := range rng.Perm(len(f.Hosts)) {
		if len(out) >= n {
			break
		}
		h := f.Hosts[idx]
		if f.planted[h] || (pred != nil && !pred(h)) {
			continue
		}
		f.planted[h] = true
		out = append(out, h)
	}
	return out
}

func hasKind(kind string) func(*Host) bool {
	return func(h *Host) bool { return h.Target(kind) != nil }
}

func (h *Host) mount(path string) *Mount {
	for _, m := range h.Mounts {
		if m.Path == path {
			return m
		}
	}
	return h.Mounts[0]
}

// alternate returns crit for even positions and warn for odd ones.
func alternate(j int) string {
	if j%2 == 0 {
		return levelCrit
	}
	return levelWarn
}

// plant turns healthy hosts into the anomalies the inspection must find. Each
// anomaly gets its own host; the resulting levels are what the default
// inspection.yaml thresholds (and the role=db override) produce.
func (f *Fleet) plant(rng *rand.Rand, group []int) {
	a := f.Scenario.Anomalies

	for j, idx := range group {
		h := f.Hosts[idx]
		m := h.Mounts[j%2]
		m.UsedPct0 = 91.5 + 5.5*rng.Float64()
		m.FillPctPerHour = 0.002
		f.record(checkDisk, h, levelCrit)
	}

	for _, h := range f.take(rng, a.HostDown, hasKind(KindMySQL)) {
		for _, t := range h.Targets {
			t.Down = true
			t.LastError = fmt.Sprintf("Get \"http://%s/metrics\": dial tcp %s: connect: no route to host", t.Instance, t.Instance)
		}
		f.record(checkTargetDown, h, levelCrit)
	}
	for _, h := range f.take(rng, a.ExporterDown, hasKind(KindMySQL)) {
		t := h.Target(KindNode)
		t.Down = true
		t.LastError = fmt.Sprintf("Get \"http://%s/metrics\": dial tcp %s: connect: connection refused", t.Instance, t.Instance)
		f.record(checkTargetDown, h, levelCrit)
	}
	for j, h := range f.take(rng, a.MySQLConn, hasKind(KindMySQL)) {
		// The connection gauge swings +-5%, so 95 stays above 90 and 85 stays in 80..90.
		h.MySQLConnPct = 95
		if alternate(j) == levelWarn {
			h.MySQLConnPct = 85
		}
		f.record(checkMySQLConn, h, alternate(j))
	}

	for _, h := range f.take(rng, a.DBDiskWarn, func(h *Host) bool { return h.Labels["role"] == "db" }) {
		m := h.mount("/data")
		m.UsedPct0 = 75.5 + 3.5*rng.Float64()
		m.FillPctPerHour = 0.001
		f.record(checkDisk, h, levelWarn)
	}
	for _, h := range f.take(rng, a.DiskFastFill, nil) {
		m := h.mount("/data")
		m.UsedPct0 = 40 + 5*rng.Float64()
		m.FillPctPerHour = 1.0
		f.record(checkDaysToFull, h, levelCrit)
	}
	for _, h := range f.take(rng, a.CPUHigh, nil) {
		h.CPUBusy = 0.96
		f.record(checkCPU, h, levelCrit)
		f.record(checkCPUPeak, h, levelCrit)
	}
	for j, h := range f.take(rng, a.MemHigh, nil) {
		h.MemPct = 98
		if alternate(j) == levelWarn {
			h.MemPct = 90
		}
		f.record(checkMem, h, alternate(j))
	}
	for j, h := range f.take(rng, a.SwapHigh, nil) {
		h.SwapSize = 2 * gib
		h.SwapPct = 90
		level := levelCrit
		if j%3 == 2 {
			h.SwapPct, level = 65, levelWarn
		}
		f.record(checkSwap, h, level)
	}
	for j, h := range f.take(rng, a.InodeHigh, nil) {
		h.mount("/data").InodeUsedPct = 95
		if alternate(j) == levelWarn {
			h.mount("/data").InodeUsedPct = 85
		}
		f.record(checkInode, h, alternate(j))
	}
	// Boots stay within 24h even after the mock has been running for half a day.
	for j, h := range f.take(rng, a.Reboot, nil) {
		h.Boot = f.Anchor.Add(-time.Duration([]float64{1.5, 4, 9}[j%3]*float64(time.Hour)) - time.Duration(j/3)*17*time.Minute)
		f.record(checkReboot, h, levelWarn)
	}
	for j, h := range f.take(rng, a.OOM, nil) {
		n, level := 7, levelCrit
		if j%2 == 1 {
			n, level = 1, levelWarn
		}
		for k := 0; k < n; k++ {
			h.OOMAt = append(h.OOMAt, f.Anchor.Add(-time.Duration(5*60-k*40-10*j)*time.Minute))
		}
		f.record(checkOOM, h, level)
	}
}

// applyVariant derives a changed fleet from the base one: appended hosts,
// moved exporters and removed hosts.
func (f *Fleet) applyVariant(counters map[string]int) {
	sc := f.Scenario
	if sc.AddHosts > 0 {
		rng := rand.New(rand.NewSource(sc.Seed + 1))
		base := len(f.Hosts)
		for i := base; i < base+sc.AddHosts; i++ {
			h := f.newHost(i, rng, counters, false)
			var kind string
			switch h.Labels["role"] {
			case "db":
				kind = KindMySQL
			case "cache":
				kind = KindRedis
			case "web":
				kind = KindNginx
			}
			if kind != "" {
				h.Targets = append(h.Targets, newTarget(kind, h.IP, f.UnknownJob))
			}
			f.Hosts = append(f.Hosts, h)
		}
	}

	byName := map[string]*Host{}
	for _, h := range f.Hosts {
		byName[h.Name] = h
	}
	for kind, names := range sc.AddExporters {
		for _, name := range names {
			if h := byName[name]; h != nil && h.Target(kind) == nil {
				h.Targets = append(h.Targets, newTarget(kind, h.IP, f.UnknownJob))
			}
		}
	}
	for kind, names := range sc.DropExporters {
		if kind == KindNode {
			continue
		}
		for _, name := range names {
			h := byName[name]
			if h == nil {
				continue
			}
			kept := h.Targets[:0]
			for _, t := range h.Targets {
				if t.Kind != kind {
					kept = append(kept, t)
				}
			}
			h.Targets = kept
		}
	}

	if len(sc.DropHosts) > 0 {
		drop := map[string]bool{}
		for _, n := range sc.DropHosts {
			drop[n] = true
		}
		kept := f.Hosts[:0]
		for _, h := range f.Hosts {
			if !drop[h.Name] {
				kept = append(kept, h)
			}
		}
		f.Hosts = kept
	}
}
