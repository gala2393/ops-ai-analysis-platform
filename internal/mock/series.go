package mock

import (
	"math"
	"strconv"
	"time"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
)

// sampleFn returns the value of a series at t; ok=false means no sample is
// scraped at that time (target down, host not booted yet).
type sampleFn func(t time.Time) (v float64, ok bool)

type seriesDef struct {
	lbls labels.Labels
	fn   sampleFn
	ref  storage.SeriesRef
}

const cpuWave = 2 * math.Pi / 1800

var cpuModes = []struct {
	name  string
	share float64
}{
	{"user", 0.62}, {"system", 0.25}, {"iowait", 0.07}, {"nice", 0.01},
	{"irq", 0.005}, {"softirq", 0.035}, {"steal", 0},
}

// splitmix returns a deterministic pseudo-random float in [-1, 1) for (salt, t).
func splitmix(salt uint64, t time.Time) float64 {
	z := salt + uint64(t.UnixMilli()/1000)*0x9e3779b97f4a7c15
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	z ^= z >> 31
	return float64(z>>11)/float64(1<<52) - 1
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func hours(t, anchor time.Time) float64 { return t.Sub(anchor).Hours() }

// cpuShape models the busy fraction of one CPU: a mean level with a slow wave.
type cpuShape struct {
	busy, amp, phase float64
}

func newCPUShape(busy, phase float64) cpuShape {
	busy = clamp(busy, 0.001, 0.995)
	return cpuShape{busy: busy, amp: math.Min(0.15, 0.9*(1-busy)/busy), phase: phase}
}

// counter is the integral of the busy rate since boot (CPU-seconds).
func (c cpuShape) counter(u float64) float64 {
	return c.busy * (u + c.amp*(math.Cos(c.phase)-math.Cos(cpuWave*u+c.phase))/cpuWave)
}

func (c cpuShape) rate(u float64) float64 {
	return c.busy * (1 + c.amp*math.Sin(cpuWave*u+c.phase))
}

// buildSeries expands the fleet into every series the mock scrapes.
func (f *Fleet) buildSeries() []*seriesDef {
	var defs []*seriesDef
	for _, h := range f.Hosts {
		for _, t := range h.Targets {
			defs = append(defs, f.targetSeries(h, t)...)
		}
	}
	return defs
}

func (f *Fleet) targetSeries(h *Host, t *Target) []*seriesDef {
	base := targetLabelPairs(h, t)
	mk := func(name string, extra ...string) labels.Labels {
		kv := make([]string, 0, len(base)+len(extra)+2)
		kv = append(kv, "__name__", name)
		kv = append(kv, base...)
		kv = append(kv, extra...)
		return labels.FromStrings(kv...)
	}
	var defs []*seriesDef
	add := func(name string, fn sampleFn, extra ...string) {
		defs = append(defs, &seriesDef{lbls: mk(name, extra...), fn: fn})
	}
	constant := func(v float64) sampleFn { return func(time.Time) (float64, bool) { return v, true } }

	up := 1.0
	if t.Down {
		up = 0
	}
	add("up", constant(up))
	if t.Down {
		// The host name must stay resolvable for a dead node exporter (asset
		// inventory style), so node_uname_info is the one series that survives.
		if t.Kind == KindNode {
			add("node_uname_info", constant(1), unameLabels(h)...)
		}
		return defs
	}

	switch t.Kind {
	case KindNode:
		f.nodeSeries(h, add)
	case KindMySQL:
		f.mysqlSeries(h, add)
	case KindRedis:
		add("redis_up", constant(1))
		add("redis_connected_clients", func(ts time.Time) (float64, bool) {
			return math.Round(40 + 10*math.Sin(2*math.Pi*float64(ts.Unix())/2400+h.phase) + 2*splitmix(h.salt+7, ts)), true
		})
	case KindNginx:
		add("nginx_up", constant(1))
		add("nginx_connections_active", func(ts time.Time) (float64, bool) {
			return math.Round(120 + 30*math.Sin(2*math.Pi*float64(ts.Unix())/1800+h.phase) + 5*splitmix(h.salt+9, ts)), true
		})
	default:
		add("billing_queue_depth", func(ts time.Time) (float64, bool) {
			return math.Round(math.Abs(25 + 20*splitmix(h.salt+11, ts))), true
		})
	}
	return defs
}

// targetLabelPairs returns the target labels Prometheus attaches to every
// series scraped from t.
func targetLabelPairs(h *Host, t *Target) []string {
	kv := []string{"job", t.Job, "instance", t.Instance, "app", t.App}
	for _, k := range labelOrder {
		if v, ok := h.Labels[k]; ok {
			kv = append(kv, k, v)
		}
	}
	return kv
}

func unameLabels(h *Host) []string {
	return []string{"nodename", h.Name, "sysname", "Linux", "release", "5.14.0-362.el9.x86_64",
		"version", "#1 SMP PREEMPT_DYNAMIC", "machine", "x86_64", "domainname", "(none)"}
}

const (
	// rebootGap is the total window of missed scrapes centred on a reboot.
	rebootGap = 3 * time.Minute
	// prevUptime is how long a rebooted host had been running before Boot.
	prevUptime = 30 * 24 * time.Hour
)

// bootAt returns the boot instant of the kernel running on h at t.
func bootAt(h *Host, t time.Time) time.Time {
	if t.Before(h.Boot) {
		return h.Boot.Add(-prevUptime)
	}
	return h.Boot
}

func inRebootGap(h *Host, t time.Time) bool {
	d := t.Sub(h.Boot)
	return d >= -rebootGap/2 && d < rebootGap/2
}

func (f *Fleet) nodeSeries(h *Host, add func(string, sampleFn, ...string)) {
	// A real reboot only costs a few missed scrapes: every series keeps its
	// history from the previous boot and counters restart at Boot.
	booted := func(fn sampleFn) sampleFn {
		return func(t time.Time) (float64, bool) {
			if inRebootGap(h, t) {
				return 0, false
			}
			return fn(t)
		}
	}
	uptime := func(t time.Time) float64 { return t.Sub(bootAt(h, t)).Seconds() }
	anchor := f.Anchor

	for c := 0; c < h.CPUs; c++ {
		shape := newCPUShape(h.CPUBusy*(1+0.05*(float64(c)-float64(h.CPUs-1)/2)), h.phase+float64(c)*0.3)
		cpu := strconv.Itoa(c)
		add("node_cpu_seconds_total", booted(func(t time.Time) (float64, bool) {
			u := uptime(t)
			return u - shape.counter(u), true
		}), "cpu", cpu, "mode", "idle")
		for _, m := range cpuModes {
			share := m.share
			add("node_cpu_seconds_total", booted(func(t time.Time) (float64, bool) {
				return share * shape.counter(uptime(t)), true
			}), "cpu", cpu, "mode", m.name)
		}
	}

	memPct := func(t time.Time) float64 {
		return clamp(h.MemPct+2*math.Sin(2*math.Pi*float64(t.Unix())/5400+h.phase)+0.3*splitmix(h.salt+1, t), 0, 99.9)
	}
	add("node_memory_MemTotal_bytes", booted(func(time.Time) (float64, bool) { return h.MemTotal, true }))
	add("node_memory_MemAvailable_bytes", booted(func(t time.Time) (float64, bool) {
		return math.Round(h.MemTotal * (1 - memPct(t)/100)), true
	}))
	add("node_memory_SwapTotal_bytes", booted(func(time.Time) (float64, bool) { return h.SwapSize, true }))
	add("node_memory_SwapFree_bytes", booted(func(t time.Time) (float64, bool) {
		pct := clamp(h.SwapPct+0.2*math.Sin(2*math.Pi*float64(t.Unix())/7200+h.phase), 0, 100)
		return math.Round(h.SwapSize * (1 - pct/100)), true
	}))

	for _, m := range h.Mounts {
		m := m
		fsl := []string{"device", m.Device, "fstype", m.FSType, "mountpoint", m.Path}
		add("node_filesystem_size_bytes", booted(func(time.Time) (float64, bool) { return m.SizeBytes, true }), fsl...)
		add("node_filesystem_avail_bytes", booted(func(t time.Time) (float64, bool) {
			used := clamp(m.UsedPct0+m.FillPctPerHour*hours(t, anchor), 0, 100)
			return math.Round(m.SizeBytes * (1 - used/100)), true
		}), fsl...)
		add("node_filesystem_files", booted(func(time.Time) (float64, bool) { return m.Files, true }), fsl...)
		add("node_filesystem_files_free", booted(func(t time.Time) (float64, bool) {
			return math.Round(m.Files * (1 - m.InodeUsedPct/100)), true
		}), fsl...)
	}

	loadShape := newCPUShape(h.CPUBusy, h.phase)
	for i, name := range []string{"node_load1", "node_load5", "node_load15"} {
		noise := []float64{0.05, 0.02, 0.01}[i]
		salt := h.salt + 20 + uint64(i)
		add(name, booted(func(t time.Time) (float64, bool) {
			return math.Max(0, float64(h.CPUs)*h.LoadMul*loadShape.rate(uptime(t))+noise*splitmix(salt, t)), true
		}))
	}

	add("node_boot_time_seconds", func(t time.Time) (float64, bool) { return float64(bootAt(h, t).Unix()), true })
	add("node_vmstat_oom_kill", booted(func(t time.Time) (float64, bool) {
		n := 0.0
		for _, at := range h.OOMAt {
			if !at.After(t) {
				n++
			}
		}
		return n, true
	}))
	add("node_uname_info", booted(func(time.Time) (float64, bool) { return 1, true }), unameLabels(h)...)

	var units []string
	if h.Systemd {
		units = []string{"sshd.service", "crond.service"}
		kinds := append([]string{}, h.SystemdOnly...)
		for _, t := range h.Targets {
			kinds = append(kinds, t.Kind)
		}
		for _, kind := range kinds {
			switch kind {
			case KindMySQL:
				units = append(units, "mysqld.service")
			case KindRedis:
				units = append(units, "redis.service")
			case KindNginx:
				units = append(units, "nginx.service")
			}
		}
	}
	for _, unit := range units {
		for _, state := range []string{"active", "inactive", "activating", "deactivating", "failed"} {
			v := 0.0
			if state == "active" {
				v = 1
			}
			add("node_systemd_unit_state", booted(func(time.Time) (float64, bool) { return v, true }),
				"name", unit, "state", state, "type", "service")
		}
	}
}

func (f *Fleet) mysqlSeries(h *Host, add func(string, sampleFn, ...string)) {
	add("mysql_up", func(time.Time) (float64, bool) { return 1, true })
	add("mysql_global_variables_max_connections", func(time.Time) (float64, bool) { return h.MySQLMaxConn, true })
	add("mysql_global_status_threads_connected", func(t time.Time) (float64, bool) {
		pct := clamp(h.MySQLConnPct*(1+0.05*math.Sin(2*math.Pi*float64(t.Unix())/1200+h.phase)), 0, 100)
		return math.Round(h.MySQLMaxConn * pct / 100), true
	})
}
