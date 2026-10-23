package mock

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"yunwei/internal/config"
)

func loadScenario(t *testing.T, rel string) *config.Scenario {
	t.Helper()
	sc, err := config.LoadScenario(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func countBy(m *Manifest) map[string]int {
	out := map[string]int{}
	for _, a := range m.Anomalies {
		out[a.CheckID]++
		out[a.CheckID+"/"+a.Level]++
	}
	return out
}

func hostByName(f *Fleet) map[string]*Host {
	out := map[string]*Host{}
	for _, h := range f.Hosts {
		out[h.Name] = h
	}
	return out
}

func TestDefaultScenarioManifestShape(t *testing.T) {
	f := BuildFleet(loadScenario(t, "config/mock-scenario.yaml"), time.Now())
	m := f.Manifest()
	hosts := hostByName(f)

	if m.Hosts != 180 || len(f.Hosts) != 180 {
		t.Fatalf("hosts = %d", m.Hosts)
	}
	want := map[string]int{
		"disk_usage/crit": 15, "disk_usage/warn": 2, "disk_days_to_full": 3, "cpu_usage": 5, "memory_usage": 4,
		"swap_usage": 3, "inode_usage": 2, "recent_reboot": 3, "oom_kills": 2, "mysql_connections": 2, "target_down": 2,
	}
	got := countBy(m)
	for k, n := range want {
		if got[k] != n {
			t.Errorf("%s = %d, want %d", k, got[k], n)
		}
	}

	var group []*Host
	for _, a := range m.Anomalies {
		if a.CheckID == "disk_usage" && a.Level == "crit" {
			group = append(group, hosts[a.Host])
		}
	}
	envs, bizs := map[string]bool{}, map[string]bool{}
	for _, h := range group {
		if h.Labels["role"] != "app" || h.Labels["dc"] != "sh1" || len(h.MissingLabels) > 0 {
			t.Errorf("group host %s has labels %v missing %v", h.Name, h.Labels, h.MissingLabels)
		}
		envs[h.Labels["env"]], bizs[h.Labels["biz"]] = true, true
	}
	if len(envs) < 2 || len(bizs) < 2 {
		t.Errorf("group must only share role/dc: envs=%v bizs=%v", envs, bizs)
	}

	causes := map[string]int{}
	for _, d := range m.Down {
		causes[d.Cause]++
		h := hosts[d.Host]
		if d.Cause == "host_down" {
			for _, tg := range h.Targets {
				if !tg.Down {
					t.Errorf("host_down %s has a live target %s", h.Name, tg.Job)
				}
			}
		} else if h.Target(KindMySQL) == nil || h.Target(KindMySQL).Down || !h.Target(KindNode).Down {
			t.Errorf("exporter_down %s: node must be down while mysql exporter is up", h.Name)
		}
	}
	if len(m.Down) != 2 || causes["host_down"] != 1 || causes["exporter_down"] != 1 {
		t.Errorf("down = %+v", m.Down)
	}
	if len(m.UnclassifiedJobs) != 1 || m.UnclassifiedJobs[0] != "legacy-billing" {
		t.Errorf("unclassified = %v", m.UnclassifiedJobs)
	}
	if len(m.MissingLabelHosts) < 5 {
		t.Errorf("missing label hosts = %v", m.MissingLabelHosts)
	}
	for _, name := range m.MissingLabelHosts {
		for _, a := range m.Anomalies {
			if a.Host == name {
				t.Errorf("planted host %s also lacks labels", name)
			}
		}
	}

	both, systemdOnly := 0, 0
	jobsByIP := map[string]map[string]bool{}
	for _, h := range f.Hosts {
		svc := strings.Join(m.Services[h.Name], ",")
		if strings.Contains(svc, "node") && strings.Contains(svc, "mysql") {
			both++
		}
		if len(h.SystemdOnly) > 0 {
			systemdOnly++
			if h.Target(h.SystemdOnly[0]) != nil || !strings.Contains(svc, h.SystemdOnly[0]) {
				t.Errorf("systemd-only host %s services %s", h.Name, svc)
			}
		}
		for _, tg := range h.Targets {
			if jobsByIP[h.IP] == nil {
				jobsByIP[h.IP] = map[string]bool{}
			}
			jobsByIP[h.IP][tg.Job] = true
		}
	}
	shared := 0
	for _, jobs := range jobsByIP {
		if len(jobs) > 1 {
			shared++
		}
	}
	if both < 10 || shared < 20 || systemdOnly != 6 {
		t.Errorf("node+mysql hosts=%d shared-ip hosts=%d systemd-only=%d", both, shared, systemdOnly)
	}

	for _, h := range f.Hosts {
		if h.Labels["role"] == "" || len(h.Labels) < 5 && len(h.MissingLabels) == 0 {
			t.Errorf("%s labels = %v", h.Name, h.Labels)
		}
	}
}

func TestDBHostDiskObservableOnlyWithOverride(t *testing.T) {
	f := BuildFleet(loadScenario(t, "config/mock-scenario.yaml"), time.Now())
	n := 0
	for _, a := range f.Manifest().Anomalies {
		if a.CheckID != "disk_usage" || a.Level != "warn" {
			continue
		}
		n++
		h := hostByName(f)[a.Host]
		if h.Labels["role"] != "db" {
			t.Errorf("%s is not a db host", h.Name)
		}
		if u := h.mount("/data").UsedPct0; u < 75 || u >= 80 {
			t.Errorf("%s disk %.2f not in [75,80)", h.Name, u)
		}
	}
	if n < 1 {
		t.Error("no db host in the 75-80 band")
	}
}

func TestSameSeedByteIdenticalManifest(t *testing.T) {
	dir := t.TempDir()
	sc := loadScenario(t, "config/mock-scenario.yaml")
	var files [2][]byte
	for i := range files {
		// Different anchors: the manifest must not depend on wall-clock time.
		f := BuildFleet(sc, time.Unix(1_700_000_000+int64(i)*3600, 0))
		p := filepath.Join(dir, "m"+strconv.Itoa(i)+".json")
		if err := f.WriteManifest(p); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		files[i] = b
	}
	if string(files[0]) != string(files[1]) {
		t.Error("manifest not byte-identical for the same seed")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(files[0], &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"hosts", "anomalies", "services", "down", "unclassified_jobs", "missing_label_hosts"} {
		if _, ok := m[k]; !ok {
			t.Errorf("manifest lacks %q", k)
		}
	}
}

func TestAssetsV2DiffersFromDefault(t *testing.T) {
	base := BuildFleet(loadScenario(t, "config/mock-scenario.yaml"), time.Now())
	v2 := BuildFleet(loadScenario(t, "config/scenarios/assets-v2.yaml"), time.Now())
	bm, vm := base.Manifest(), v2.Manifest()

	added, removed, changed := 0, 0, 0
	for name, svc := range vm.Services {
		old, ok := bm.Services[name]
		switch {
		case !ok:
			added++
		case strings.Join(old, ",") != strings.Join(svc, ","):
			changed++
		}
	}
	for name := range bm.Services {
		if _, ok := vm.Services[name]; !ok {
			removed++
		}
	}
	if added < 3 || removed < 3 || changed < 3 {
		t.Errorf("added=%d removed=%d service-changed=%d", added, removed, changed)
	}
	if len(base.Manifest().Anomalies) != len(vm.Anomalies) {
		t.Errorf("planted anomalies must survive the variant: %d vs %d", len(bm.Anomalies), len(vm.Anomalies))
	}
	byName := hostByName(base)
	for _, n := range v2.Scenario.DropHosts {
		if byName[n] == nil || base.planted[byName[n]] {
			t.Errorf("drop_hosts %s must be an unplanted default host", n)
		}
	}
	for _, m := range []map[string][]string{v2.Scenario.AddExporters, v2.Scenario.DropExporters} {
		for _, names := range m {
			for _, n := range names {
				if byName[n] == nil {
					t.Errorf("exporter change references unknown host %s", n)
				}
			}
		}
	}
	names := make([]string, 0, len(v2.Hosts))
	for _, h := range v2.Hosts {
		names = append(names, h.Name)
	}
	sort.Strings(names)
	for i := 1; i < len(names); i++ {
		if names[i] == names[i-1] {
			t.Errorf("duplicate host name %s", names[i])
		}
	}
}

type finding struct{ check, host, level string }

// TestManifestMatchesDefaultInspection runs every default inspection item as
// raw PromQL against the mock and requires exactly the manifest's findings.
func TestManifestMatchesDefaultInspection(t *testing.T) {
	if testing.Short() {
		t.Skip("full 180-host mock")
	}
	sc := loadScenario(t, "config/mock-scenario.yaml")
	sc.LiveInterval = 0
	s, ts := startMock(t, sc, nil)
	insp, err := config.LoadInspection(filepath.Join("..", "..", "config", "inspection.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	if v := queryVector(t, ts.URL, `count(count by (nodename) (node_uname_info))`).Result[0].Value[1]; v != "180" {
		t.Errorf("distinct nodenames = %v, want 180 (down hosts stay resolvable)", v)
	}

	byIP := map[string]*Host{}
	for _, h := range s.fleet.Hosts {
		byIP[h.IP] = h
	}
	rank := map[string]int{"": 0, levelWarn: 1, levelCrit: 2}
	got := map[finding]bool{}
	worst := map[[2]string]string{}
	for _, it := range insp.Items {
		for _, r := range queryVector(t, ts.URL, it.Query).Result {
			inst := r.Metric["instance"]
			h := byIP[inst[:strings.LastIndex(inst, ":")]]
			if h == nil {
				t.Fatalf("%s: unknown instance %q", it.ID, inst)
			}
			if len(it.AppliesTo) > 0 && h.Target(it.AppliesTo[0]) == nil {
				continue
			}
			v, _ := strconv.ParseFloat(r.Value[1].(string), 64)
			warn, crit := it.Warn, it.Crit
			for _, ov := range insp.Overrides {
				if ov.Match["role"] != h.Labels["role"] {
					continue
				}
				if o, ok := ov.Items[it.ID]; ok {
					if o.Warn != nil {
						warn = o.Warn
					}
					if o.Crit != nil {
						crit = o.Crit
					}
				}
			}
			level := ""
			if crit != nil && compare(it.Compare, v, *crit) {
				level = levelCrit
			} else if warn != nil && compare(it.Compare, v, *warn) {
				level = levelWarn
			}
			key := [2]string{it.ID, h.Name}
			if rank[level] > rank[worst[key]] {
				worst[key] = level
			}
		}
	}
	for k, level := range worst {
		if level != "" {
			got[finding{k[0], k[1], level}] = true
		}
	}
	want := map[finding]bool{}
	for _, a := range s.fleet.Manifest().Anomalies {
		want[finding{a.CheckID, a.Host, a.Level}] = true
	}
	for f := range want {
		if !got[f] {
			t.Errorf("planted anomaly not found by inspection: %+v", f)
		}
	}
	for f := range got {
		if !want[f] {
			t.Errorf("unplanted finding: %+v", f)
		}
	}
}

func compare(op string, v, threshold float64) bool {
	switch op {
	case ">":
		return v > threshold
	case ">=":
		return v >= threshold
	case "<":
		return v < threshold
	case "<=":
		return v <= threshold
	case "==":
		return v == threshold
	case "!=":
		return v != threshold
	}
	return false
}
