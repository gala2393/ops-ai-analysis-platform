// Package mock implements a mock Prometheus: a scenario-driven synthetic host
// fleet stored in an in-memory TSDB and queried through the real PromQL engine.
package mock

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"time"

	"yunwei/internal/config"
)

// Kinds of scrape targets the fleet can expose.
const (
	KindNode    = "node"
	KindMySQL   = "mysql"
	KindRedis   = "redis"
	KindNginx   = "nginx"
	KindUnknown = "unknown"
)

const (
	gib      = 1 << 30
	nodePort = 9100
)

// labelOrder is the fixed order in which enterprise labels are drawn so a seed
// always yields the same fleet.
var labelOrder = []string{"env", "role", "biz", "dc", "owner"}

var defaultLabelPools = map[string][]string{
	"env":   {"prod", "staging"},
	"role":  {"app", "db", "cache", "web"},
	"biz":   {"pay", "order", "user", "search"},
	"dc":    {"bj1", "sh1", "gz1"},
	"owner": {"ops-a", "ops-b", "ops-c"},
}

// Mount describes one filesystem exposed by node_exporter.
type Mount struct {
	Path, FSType, Device string
	SizeBytes            float64
	// UsedPct0 is the usage percentage at the fleet anchor time; usage then
	// moves linearly by FillPctPerHour.
	UsedPct0       float64
	FillPctPerHour float64
	Files          float64
	InodeUsedPct   float64
}

// Target is one scrape target (exporter endpoint) of a host.
type Target struct {
	Kind      string
	Job       string
	App       string
	Port      int
	Instance  string
	Down      bool
	LastError string
}

// Host is one machine of the fleet together with its synthetic behaviour.
type Host struct {
	Index         int
	Name          string
	IP            string
	Labels        map[string]string // enterprise labels actually present
	MissingLabels []string
	Targets       []*Target

	CPUs     int
	CPUBusy  float64 // mean busy fraction 0..1
	MemTotal float64
	MemPct   float64
	SwapSize float64
	SwapPct  float64
	LoadMul  float64
	Mounts   []*Mount
	Boot     time.Time
	OOMAt    []time.Time

	MySQLMaxConn float64
	MySQLConnPct float64

	// SystemdOnly lists services running under systemd without an exporter.
	SystemdOnly []string
	// Systemd is true when node_exporter exposes node_systemd_unit_state.
	Systemd bool

	phase float64
	salt  uint64
}

// Services lists the service ids this host runs, derived from its exporters
// and from systemd units of services that have no exporter.
func (h *Host) Services() []string {
	var out []string
	seen := map[string]bool{}
	add := func(kind string) {
		if !seen[kind] {
			seen[kind] = true
			out = append(out, kind)
		}
	}
	for _, t := range h.Targets {
		switch t.Kind {
		case KindNode, KindMySQL, KindRedis, KindNginx:
			add(t.Kind)
		}
	}
	for _, kind := range h.SystemdOnly {
		add(kind)
	}
	return out
}

// Target returns the host's target of a kind, or nil.
func (h *Host) Target(kind string) *Target {
	for _, t := range h.Targets {
		if t.Kind == kind {
			return t
		}
	}
	return nil
}

// Fleet is the complete synthetic estate for a scenario.
type Fleet struct {
	Scenario   *config.Scenario
	Anchor     time.Time
	Hosts      []*Host
	UnknownJob string
	// Planted is the ground truth of every finding the default inspection
	// items produce on this fleet.
	Planted []Anomaly

	planted map[*Host]bool
}

// BuildFleet deterministically generates the fleet for a scenario. anchor is
// the "now" the generated trends are expressed relative to.
func BuildFleet(sc *config.Scenario, anchor time.Time) *Fleet {
	rng := rand.New(rand.NewSource(sc.Seed))
	plantRNG := rand.New(rand.NewSource(sc.Seed ^ 0x5eed1e55))
	f := &Fleet{Scenario: sc, Anchor: anchor, UnknownJob: sc.UnknownJob, planted: map[*Host]bool{}}
	if f.UnknownJob == "" {
		f.UnknownJob = "legacy-billing"
	}

	group := f.diskGroupIndexes(plantRNG)
	inGroup := map[int]bool{}
	for _, idx := range group {
		inGroup[idx] = true
	}
	counters := map[string]int{}
	for i := 0; i < sc.Hosts; i++ {
		f.Hosts = append(f.Hosts, f.newHost(i, rng, counters, inGroup[i]))
	}

	f.assignExporters(rng)
	f.assignSystemdOnly(rng)
	f.plant(plantRNG, group)
	f.dropLabels(rng)
	f.applyVariant(counters)
	for _, h := range f.Hosts {
		h.Systemd = len(h.Targets) > 1 || len(h.SystemdOnly) > 0 || h.Index%3 == 0
	}
	return f
}

func (f *Fleet) pool(key string) []string {
	if v := f.Scenario.Labels[key]; len(v) > 0 {
		return v
	}
	return defaultLabelPools[key]
}

// newHost draws a plain healthy host: labels, node target and resources.
func (f *Fleet) newHost(i int, rng *rand.Rand, counters map[string]int, diskGroup bool) *Host {
	h := &Host{Index: i, Labels: map[string]string{}, salt: uint64(i)*0x9e3779b97f4a7c15 + uint64(f.Scenario.Seed)}
	for _, k := range labelOrder {
		vals := f.pool(k)
		h.Labels[k] = vals[rng.Intn(len(vals))]
	}
	if diskGroup {
		h.Labels["role"], h.Labels["dc"] = f.groupRole(), f.groupDC()
	}
	key := h.Labels["role"] + "-" + h.Labels["dc"]
	counters[key]++
	h.Name = fmt.Sprintf("%s-%03d", key, counters[key])
	h.IP = fmt.Sprintf("10.20.%d.%d", 1+i/250, 1+i%250)
	h.Targets = []*Target{newTarget(KindNode, h.IP, f.UnknownJob)}
	f.fillResources(h, rng)
	return h
}

func newTarget(kind, ip, unknownJob string) *Target {
	t := &Target{Kind: kind}
	switch kind {
	case KindNode:
		t.Job, t.App, t.Port = "node", "node-exporter", nodePort
	case KindMySQL:
		t.Job, t.App, t.Port = "mysql", "mysql_exporter", 9104
	case KindRedis:
		t.Job, t.App, t.Port = "redis", "redis_exporter", 9121
	case KindNginx:
		t.Job, t.App, t.Port = "nginx", "nginx_exporter", 9113
	default:
		t.Job, t.App, t.Port = unknownJob, "billing-exporter", 9777
	}
	t.Instance = ip + ":" + strconv.Itoa(t.Port)
	return t
}

func (f *Fleet) fillResources(h *Host, rng *rand.Rand) {
	h.CPUs = 2
	h.CPUBusy = 0.10 + 0.30*rng.Float64()
	h.MemTotal = []float64{8, 16, 32}[rng.Intn(3)] * gib
	h.MemPct = 30 + 35*rng.Float64()
	h.SwapSize = 2 * gib
	if h.Index%7 == 3 {
		h.SwapSize = 0
	}
	h.SwapPct = 8 * rng.Float64()
	h.LoadMul = 0.8 + 0.4*rng.Float64()
	h.phase = 2 * math.Pi * rng.Float64()

	h.Mounts = []*Mount{
		newMount("/", "xfs", "/dev/mapper/rhel-root", (50+50*rng.Float64())*gib, 25+35*rng.Float64(), rng),
		newMount("/data", "xfs", "/dev/vdb1", (200+300*rng.Float64())*gib, 20+45*rng.Float64(), rng),
		{Path: "/run", FSType: "tmpfs", Device: "tmpfs", SizeBytes: 4 * gib, UsedPct0: 1, Files: 1048576, InodeUsedPct: 0.1},
	}
	uptimeHours := 240 + 2640*rng.Float64()
	h.Boot = f.Anchor.Add(-time.Duration(uptimeHours * float64(time.Hour)))
	h.MySQLMaxConn = 500
	h.MySQLConnPct = 10 + 35*rng.Float64()
}

func newMount(path, fstype, dev string, size, used float64, rng *rand.Rand) *Mount {
	return &Mount{
		Path: path, FSType: fstype, Device: dev, SizeBytes: size,
		UsedPct0:       used,
		FillPctPerHour: 0.001 + 0.003*rng.Float64(),
		Files:          math.Floor(size / 16384),
		InodeUsedPct:   2 + 23*rng.Float64(),
	}
}

// dropLabels removes some enterprise labels from a fraction of hosts, mimicking
// targets that were onboarded without the full label set.
// Planted hosts keep their full label set so their anomalies stay unambiguous.
func (f *Fleet) dropLabels(rng *rand.Rand) {
	var candidates []*Host
	for _, h := range f.Hosts {
		if !f.planted[h] {
			candidates = append(candidates, h)
		}
	}
	n := int(math.Round(f.Scenario.MissingLabelFraction * float64(len(f.Hosts))))
	if n > len(candidates) {
		n = len(candidates)
	}
	if n == 0 {
		return
	}
	droppable := []string{"env", "biz", "dc", "owner"}
	for _, idx := range rng.Perm(len(candidates))[:n] {
		h := candidates[idx]
		k := 1 + rng.Intn(2)
		for _, di := range rng.Perm(len(droppable))[:k] {
			label := droppable[di]
			delete(h.Labels, label)
			h.MissingLabels = append(h.MissingLabels, label)
		}
		sort.Strings(h.MissingLabels)
	}
}

// assignExporters attaches the extra exporters, preferring hosts whose role
// matches the exporter's service.
func (f *Fleet) assignExporters(rng *rand.Rand) {
	sc := f.Scenario
	pick := func(role string, n int) []*Host {
		var preferred, others []*Host
		for _, h := range f.Hosts {
			if h.Labels["role"] == role {
				preferred = append(preferred, h)
			} else {
				others = append(others, h)
			}
		}
		rng.Shuffle(len(others), func(i, j int) { others[i], others[j] = others[j], others[i] })
		all := append(preferred, others...)
		if n > len(all) {
			n = len(all)
		}
		return all[:n]
	}
	for _, x := range []struct {
		kind, role string
		n          int
	}{
		{KindMySQL, "db", sc.MySQLHosts},
		{KindRedis, "cache", sc.RedisHosts},
		{KindNginx, "web", sc.NginxHosts},
		{KindUnknown, "", sc.UnknownHosts},
	} {
		for _, h := range pick(x.role, x.n) {
			h.Targets = append(h.Targets, newTarget(x.kind, h.IP, f.UnknownJob))
		}
	}
}

// assignSystemdOnly makes some single-exporter hosts run a service that only
// node_systemd_unit_state reveals.
func (f *Fleet) assignSystemdOnly(rng *rand.Rand) {
	kinds := []struct{ kind, role string }{{KindMySQL, "db"}, {KindRedis, "cache"}, {KindNginx, "web"}}
	for j := 0; j < f.Scenario.SystemdOnlyHosts; j++ {
		x := kinds[j%len(kinds)]
		var preferred, others []*Host
		for _, h := range f.Hosts {
			if len(h.Targets) != 1 || len(h.SystemdOnly) > 0 {
				continue
			}
			if h.Labels["role"] == x.role {
				preferred = append(preferred, h)
			} else {
				others = append(others, h)
			}
		}
		all := append(preferred, others...)
		if len(all) == 0 {
			return
		}
		pickIdx := rng.Intn(len(preferred) + len(others))
		if len(preferred) > 0 {
			pickIdx = rng.Intn(len(preferred))
		}
		all[pickIdx].SystemdOnly = []string{x.kind}
	}
}

// TargetCount is the number of scrape targets (and therefore up series).
func (f *Fleet) TargetCount() int {
	n := 0
	for _, h := range f.Hosts {
		n += len(h.Targets)
	}
	return n
}

// MissingLabelHosts lists the names of hosts lacking enterprise labels.
func (f *Fleet) MissingLabelHosts() []string {
	var out []string
	for _, h := range f.Hosts {
		if len(h.MissingLabels) > 0 {
			out = append(out, h.Name)
		}
	}
	return out
}
