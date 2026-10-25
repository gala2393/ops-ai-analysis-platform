package discovery

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"

	"yunwei/internal/config"
)

const (
	upQuery    = "up"
	unameQuery = "max by (instance, nodename) (node_uname_info)"
)

// HostKey strips the port from a scrape instance ("10.0.0.1:9100" -> "10.0.0.1").
func HostKey(instance string) string {
	if h, _, err := net.SplitHostPort(instance); err == nil {
		return h
	}
	return strings.TrimSpace(instance)
}

type rawTarget struct {
	Target
	labels map[string]string
}

type hostBuild struct {
	key     string
	targets []rawTarget
	uname   string
	hits    map[string]map[string]bool // service id -> sources
}

// Discover builds the host inventory. Query count is 2 (targets, up) plus one
// for node_uname_info, one for systemd (when configured) and one per service
// rule that has metric prefixes.
func Discover(ctx context.Context, src Source, svc *config.Services, lc config.LabelsConfig, ts time.Time) (*Result, error) {
	if ts.IsZero() {
		ts = time.Now()
	}
	res := &Result{}
	warn := func(format string, args ...any) { res.Warnings = append(res.Warnings, fmt.Sprintf(format, args...)) }

	targets, err := loadTargets(ctx, src, ts, res, warn)
	if err != nil {
		return nil, err
	}

	hosts := map[string]*hostBuild{}
	for _, t := range targets {
		key := HostKey(t.Instance)
		hb := hosts[key]
		if hb == nil {
			hb = &hostBuild{key: key, hits: map[string]map[string]bool{}}
			hosts[key] = hb
		}
		hb.targets = append(hb.targets, t)
	}

	hit := func(key, id, source string) {
		hb := hosts[key]
		if hb == nil {
			return
		}
		if hb.hits[id] == nil {
			hb.hits[id] = map[string]bool{}
		}
		hb.hits[id][source] = true
	}

	classifiedJobs := map[string]bool{}
	for _, hb := range hosts {
		for _, t := range hb.targets {
			for _, rule := range svc.Services {
				if matchesAny(t.Job, rule.Match.Jobs) {
					hit(hb.key, rule.ID, SourceJob)
					classifiedJobs[t.Job] = true
				}
				if matchesAny(t.App, rule.Match.Apps) {
					hit(hb.key, rule.ID, SourceApp)
					classifiedJobs[t.Job] = true
				}
			}
		}
	}

	for _, rule := range svc.Services {
		if len(rule.Match.MetricPrefixes) == 0 {
			continue
		}
		vec, err := queryVector(ctx, src, metricPrefixQuery(rule.Match.MetricPrefixes), ts, res)
		if err != nil {
			warn("服务 %s 的指标前缀探测失败: %v", rule.ID, err)
			continue
		}
		for _, s := range vec {
			hit(HostKey(string(s.Metric["instance"])), rule.ID, SourceMetric)
			if job := string(s.Metric["job"]); job != "" {
				classifiedJobs[job] = true
			}
		}
	}

	if len(svc.SystemdUnits.Map) > 0 && svc.SystemdUnits.Metric != "" {
		vec, err := queryVector(ctx, src, systemdQuery(svc.SystemdUnits), ts, res)
		if err != nil {
			warn("systemd 单元探测失败: %v", err)
		}
		for _, s := range vec {
			if id, ok := svc.SystemdUnits.Map[string(s.Metric["name"])]; ok {
				hit(HostKey(string(s.Metric["instance"])), id, SourceSystemd)
			}
		}
	}

	if vec, err := queryVector(ctx, src, unameQuery, ts, res); err != nil {
		warn("node_uname_info 查询失败，主机名回退为 IP: %v", err)
	} else {
		for _, s := range vec {
			if hb := hosts[HostKey(string(s.Metric["instance"]))]; hb != nil && hb.uname == "" {
				hb.uname = string(s.Metric["nodename"])
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	for _, hb := range hosts {
		res.Hosts = append(res.Hosts, buildHost(hb, svc, lc))
	}
	sort.Slice(res.Hosts, func(i, j int) bool { return res.Hosts[i].Key < res.Hosts[j].Key })
	res.Unclassified = unclassified(res.Hosts, classifiedJobs)
	return res, nil
}

func matchesAny(value string, patterns []string) bool {
	if value == "" {
		return false
	}
	lv := strings.ToLower(value)
	for _, p := range patterns {
		if p != "" && strings.Contains(lv, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

func metricPrefixQuery(prefixes []string) string {
	alts := make([]string, len(prefixes))
	for i, p := range prefixes {
		alts[i] = regexp.QuoteMeta(p) + ".+"
	}
	return fmt.Sprintf(`count by (instance, job) ({__name__=~%q})`, strings.Join(alts, "|"))
}

func systemdQuery(u config.SystemdUnits) string {
	units := make([]string, 0, len(u.Map))
	for name := range u.Map {
		units = append(units, regexp.QuoteMeta(name))
	}
	sort.Strings(units)
	return fmt.Sprintf(`%s{name=~%q,state="active"} == 1`, u.Metric, strings.Join(units, "|"))
}

func queryVector(ctx context.Context, src Source, q string, ts time.Time, res *Result) (model.Vector, error) {
	res.Queries++
	v, _, err := src.Query(ctx, q, ts)
	if err != nil {
		return nil, err
	}
	vec, ok := v.(model.Vector)
	if !ok {
		return nil, fmt.Errorf("unexpected result type %s", v.Type())
	}
	return vec, nil
}

// loadTargets reads the targets API and the up metric. Either may fail as long
// as the other succeeds; up fills in targets the targets API did not list.
func loadTargets(ctx context.Context, src Source, ts time.Time, res *Result, warn func(string, ...any)) ([]rawTarget, error) {
	var out []rawTarget
	seen := map[string]bool{}
	id := func(job, instance string) string { return job + "\x00" + instance }

	res.Queries++
	tr, terr := src.Targets(ctx)
	if terr != nil {
		warn("targets 接口不可用，仅使用 up 指标: %v", terr)
	}
	for _, at := range tr.Active {
		labels := map[string]string{}
		for k, v := range at.Labels {
			labels[string(k)] = string(v)
		}
		instance := labels["instance"]
		if instance == "" {
			if u, err := url.Parse(at.ScrapeURL); err == nil {
				instance = u.Host
			}
		}
		if instance == "" {
			continue
		}
		job := labels["job"]
		if job == "" {
			job = at.ScrapePool
		}
		out = append(out, rawTarget{
			Target: Target{Job: job, App: labels["app"], Instance: instance,
				Up: at.Health == promv1.HealthGood, LastError: at.LastError},
			labels: labels,
		})
		seen[id(job, instance)] = true
	}

	vec, uerr := queryVector(ctx, src, upQuery, ts, res)
	if uerr != nil {
		warn("up 查询失败: %v", uerr)
		if terr != nil {
			return nil, fmt.Errorf("无法获取 targets 与 up: %w", uerr)
		}
	}
	for _, s := range vec {
		labels := map[string]string{}
		for k, v := range s.Metric {
			labels[string(k)] = string(v)
		}
		instance, job := labels["instance"], labels["job"]
		if instance == "" || seen[id(job, instance)] {
			continue
		}
		out = append(out, rawTarget{
			Target: Target{Job: job, App: labels["app"], Instance: instance, Up: s.Value == 1},
			labels: labels,
		})
		seen[id(job, instance)] = true
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func buildHost(hb *hostBuild, svc *config.Services, lc config.LabelsConfig) *Host {
	sort.Slice(hb.targets, func(i, j int) bool {
		a, b := hb.targets[i], hb.targets[j]
		if a.Job != b.Job {
			return a.Job < b.Job
		}
		return a.Instance < b.Instance
	})
	h := &Host{Key: hb.key, IPs: []string{hb.key}, Labels: map[string]string{}, MissingLabels: []string{}}

	mapping := lc.Fields()
	hostLabel := lc.Hostname
	var configuredName string
	for _, t := range hb.targets {
		h.Targets = append(h.Targets, t.Target)
		for _, field := range LabelFields {
			name := mapping[field]
			if name == "" {
				name = field
			}
			if v := t.labels[name]; v != "" && h.Labels[field] == "" {
				h.Labels[field] = v
			}
		}
		if hostLabel != "" && configuredName == "" {
			configuredName = t.labels[hostLabel]
		}
	}
	for _, field := range LabelFields {
		if h.Labels[field] == "" {
			h.MissingLabels = append(h.MissingLabels, field)
		}
	}

	switch {
	case configuredName != "":
		h.Name = configuredName
	case hb.uname != "":
		h.Name = hb.uname
	default:
		h.Name = hb.key
	}

	for _, rule := range svc.Services {
		if srcs := hb.hits[rule.ID]; len(srcs) > 0 {
			s := Service{ID: rule.ID}
			for _, name := range []string{SourceJob, SourceApp, SourceMetric, SourceSystemd} {
				if srcs[name] {
					s.Sources = append(s.Sources, name)
				}
			}
			h.Services = append(h.Services, s)
		}
	}
	sort.Slice(h.Services, func(i, j int) bool { return h.Services[i].ID < h.Services[j].ID })

	classifyDown(h)
	return h
}

// classifyDown sets the status and cause. Any down target makes the host
// non-normal: all targets down means the machine or network is gone, only
// some down means the exporter itself died.
func classifyDown(h *Host) {
	h.Status = StatusNormal
	down := 0
	for _, t := range h.Targets {
		if t.Up {
			continue
		}
		down++
		h.DownJobs = appendUnique(h.DownJobs, t.Job)
		if h.DownError == "" {
			h.DownError = t.LastError
		}
	}
	switch {
	case down == 0:
		return
	case down == len(h.Targets):
		h.Status, h.DownCause, h.DownText = StatusHostDown, CauseHostDown, CauseHostDownText
	default:
		h.Status, h.DownCause, h.DownText = StatusExporterDown, CauseExporterDown, CauseExporterDownText
	}
	le := strings.ToLower(h.DownError)
	h.NetworkHint = strings.Contains(le, "no route to host") || strings.Contains(le, "timeout")
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func unclassified(hosts []*Host, classified map[string]bool) []UnclassifiedJob {
	byJob := map[string]*UnclassifiedJob{}
	for _, h := range hosts {
		for _, t := range h.Targets {
			if t.Job == "" || classified[t.Job] {
				continue
			}
			u := byJob[t.Job]
			if u == nil {
				u = &UnclassifiedJob{Job: t.Job}
				byJob[t.Job] = u
			}
			u.Targets++
			u.Hosts = appendUnique(u.Hosts, h.Key)
		}
	}
	out := make([]UnclassifiedJob, 0, len(byJob))
	for _, u := range byJob {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Job < out[j].Job })
	return out
}
