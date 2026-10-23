package config

import (
	"strings"
	"time"
)

// Scenario drives the mock Prometheus generator. Unknown keys are tolerated so
// scenario files can carry generator-specific sections.
type Scenario struct {
	Hosts        int           `yaml:"hosts"`
	Seed         int64         `yaml:"seed"`
	History      time.Duration `yaml:"history"`
	Step         time.Duration `yaml:"step"`
	LiveInterval time.Duration `yaml:"live_interval"`

	// MissingLabelFraction is the share of hosts that lack some enterprise labels.
	MissingLabelFraction float64 `yaml:"missing_label_fraction"`

	Labels map[string][]string `yaml:"labels"`

	// Extra exporters running next to node_exporter on existing hosts.
	MySQLHosts   int `yaml:"mysql_hosts"`
	RedisHosts   int `yaml:"redis_hosts"`
	NginxHosts   int `yaml:"nginx_hosts"`
	UnknownHosts int `yaml:"unknown_exporter_hosts"`
	// UnknownJob is the job name used for exporters no service rule recognises.
	UnknownJob string `yaml:"unknown_job"`

	// SystemdOnlyHosts run mysql/redis/nginx under systemd without any exporter,
	// so only node_systemd_unit_state reveals the service.
	SystemdOnlyHosts int `yaml:"systemd_only_hosts"`

	Anomalies Anomalies `yaml:"anomalies"`

	// The fields below derive a variant fleet from the same seed: hosts are
	// removed or appended and exporters moved, which exercises asset diffing.
	DropHosts     []string            `yaml:"drop_hosts"`
	AddHosts      int                 `yaml:"add_hosts"`
	AddExporters  map[string][]string `yaml:"add_exporters"`
	DropExporters map[string][]string `yaml:"drop_exporters"`
}

// Anomalies counts the problems planted into the generated fleet. Every
// planted host is distinct, so each count maps to its own set of hosts.
type Anomalies struct {
	// DiskGroup hosts share DiskGroupRole/DiskGroupDC and have a nearly full
	// disk; they must be reported as one problem group.
	DiskGroup     int    `yaml:"disk_group"`
	DiskGroupRole string `yaml:"disk_group_role"`
	DiskGroupDC   string `yaml:"disk_group_dc"`
	DiskFastFill  int    `yaml:"disk_fast_fill"`
	// DBDiskWarn db hosts sit between the role=db warn override (75) and the
	// default warn threshold (80).
	DBDiskWarn   int `yaml:"db_disk_warn"`
	CPUHigh      int `yaml:"cpu_high"`
	MemHigh      int `yaml:"mem_high"`
	SwapHigh     int `yaml:"swap_high"`
	InodeHigh    int `yaml:"inode_high"`
	Reboot       int `yaml:"reboot"`
	OOM          int `yaml:"oom"`
	MySQLConn    int `yaml:"mysql_conn"`
	HostDown     int `yaml:"host_down"`
	ExporterDown int `yaml:"exporter_down"`
}

// Total is the number of distinct hosts the plan needs.
func (a Anomalies) Total() int {
	return a.DiskGroup + a.DiskFastFill + a.DBDiskWarn + a.CPUHigh + a.MemHigh + a.SwapHigh +
		a.InodeHigh + a.Reboot + a.OOM + a.MySQLConn + a.HostDown + a.ExporterDown
}

// LoadScenario loads and validates a mock scenario file.
func LoadScenario(path string) (*Scenario, error) {
	sc := &Scenario{History: 6 * time.Hour, Step: 60 * time.Second, LiveInterval: 30 * time.Second}
	if _, err := readYAML(path, sc, false); err != nil {
		return nil, err
	}
	p := &problems{file: path}
	if sc.Hosts < 1 {
		p.add("hosts", "必须 >= 1 (must be >= 1)")
	}
	if sc.History <= 0 {
		p.add("history", "必须大于 0 (must be > 0)")
	}
	if sc.Step <= 0 {
		p.add("step", "必须大于 0 (must be > 0)")
	}
	if sc.History > 0 && sc.Step > 0 && sc.Step > sc.History {
		p.add("step", "不能大于 history (step must not exceed history)")
	}
	for _, f := range []struct {
		key string
		n   int
	}{{"mysql_hosts", sc.MySQLHosts}, {"redis_hosts", sc.RedisHosts}, {"nginx_hosts", sc.NginxHosts}, {"unknown_exporter_hosts", sc.UnknownHosts}} {
		if f.n < 0 || f.n > sc.Hosts {
			p.add(f.key, "必须在 0 到 hosts 之间 (must be within 0..hosts)")
		}
	}
	a := sc.Anomalies
	for key, n := range map[string]int{
		"systemd_only_hosts": sc.SystemdOnlyHosts, "add_hosts": sc.AddHosts,
		"anomalies.disk_group": a.DiskGroup, "anomalies.disk_fast_fill": a.DiskFastFill,
		"anomalies.db_disk_warn": a.DBDiskWarn, "anomalies.cpu_high": a.CPUHigh, "anomalies.mem_high": a.MemHigh,
		"anomalies.swap_high": a.SwapHigh, "anomalies.inode_high": a.InodeHigh, "anomalies.reboot": a.Reboot,
		"anomalies.oom": a.OOM, "anomalies.mysql_conn": a.MySQLConn, "anomalies.host_down": a.HostDown,
		"anomalies.exporter_down": a.ExporterDown,
	} {
		if n < 0 {
			p.add(key, "不能为负数 (must not be negative)")
		}
	}
	if a.Total()+sc.SystemdOnlyHosts > sc.Hosts {
		p.add("anomalies", "植入的异常主机数超过 hosts (planted hosts exceed hosts)")
	}
	if a.HostDown+a.ExporterDown+a.MySQLConn > sc.MySQLHosts {
		p.add("anomalies", "host_down+exporter_down+mysql_conn 不能超过 mysql_hosts (needs mysql hosts)")
	}
	if sc.LiveInterval < 0 {
		p.add("live_interval", "不能为负数 (must not be negative)")
	}
	if sc.MissingLabelFraction < 0 || sc.MissingLabelFraction > 1 {
		p.add("missing_label_fraction", "必须在 0 到 1 之间 (must be within 0..1)")
	}
	for name, vals := range sc.Labels {
		if len(vals) == 0 || strings.TrimSpace(name) == "" {
			p.add("labels."+name, "取值列表不能为空 (value list must not be empty)")
		}
	}
	if err := p.err(); err != nil {
		return nil, err
	}
	return sc, nil
}
