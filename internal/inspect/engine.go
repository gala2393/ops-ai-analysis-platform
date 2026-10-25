package inspect

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"

	"yunwei/internal/config"
	"yunwei/internal/discovery"
)

// Source is the subset of the prom client the engine needs.
type Source interface {
	Query(ctx context.Context, query string, ts time.Time) (model.Value, promv1.Warnings, error)
}

// Options tunes one evaluation.
type Options struct {
	// Concurrency bounds items queried in parallel (default 4).
	Concurrency int
	// MaxRange and MinStep bound query windows (defaults 24h and 60s).
	MaxRange time.Duration
	MinStep  time.Duration
	// TopN is the length of each ranking (default 10).
	TopN int
	// Now is the evaluation time (default time.Now()).
	Now time.Time
	// History maps a host key to the number of earlier runs in which it had findings.
	History map[string]int
	// OnProgress is called after each item finishes.
	OnProgress func(done, total int)
}

func (o Options) withDefaults() Options {
	if o.Concurrency < 1 {
		o.Concurrency = 4
	}
	if o.MaxRange <= 0 {
		o.MaxRange = 24 * time.Hour
	}
	if o.MinStep <= 0 {
		o.MinStep = 60 * time.Second
	}
	if o.TopN < 1 {
		o.TopN = 10
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	return o
}

// hostValue is the most extreme sample of a host for one item.
type hostValue struct {
	hostKey string
	value   float64
	labels  map[string]string
	level   string
	// crossed, warn, crit, overridden describe the threshold used for this sample.
	warn, crit *float64
	crossed    float64
	overridden bool
	breaching  int
}

type itemOutcome struct {
	run    ItemRun
	values map[string]*hostValue // by host key
	unmap  int
	// queried is true once the query was sent to Prometheus.
	queried bool
}

// Run evaluates every enabled item against src for the discovered hosts.
// One failing item is recorded in Result.Items and never aborts the others.
func Run(ctx context.Context, src Source, in *config.Inspection, disc *discovery.Result, opts Options) (*Result, error) {
	opts = opts.withDefaults()
	items := in.EnabledItems()
	hosts := map[string]*discovery.Host{}
	for _, h := range disc.Hosts {
		hosts[h.Key] = h
	}

	outcomes := make([]itemOutcome, len(items))
	sem := make(chan struct{}, opts.Concurrency)
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		done int
	)
	for i := range items {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				outcomes[i] = itemOutcome{run: failedRun(items[i], i, ctx.Err())}
				return
			}
			defer func() { <-sem }()
			outcomes[i] = evalItem(ctx, src, items[i], i, in.Overrides, hosts, opts)
			if opts.OnProgress != nil {
				mu.Lock()
				done++
				d := done
				mu.Unlock()
				opts.OnProgress(d, len(items))
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	res := &Result{}
	values := map[string]map[string]*hostValue{}
	itemByID := map[string]config.Item{}
	for i, oc := range outcomes {
		it := items[i]
		itemByID[it.ID] = it
		values[it.ID] = oc.values
		res.Unmapped += oc.unmap
		if oc.queried {
			res.Queries++
		}
		for _, hv := range oc.values {
			if hv.level == LevelOK {
				continue
			}
			f := buildFinding(it, oc.run, hosts[hv.hostKey], hv)
			res.Findings = append(res.Findings, f)
			oc.run.Findings++
		}
		res.Items = append(res.Items, oc.run)
	}
	sortFindings(res.Findings)
	res.Groups = buildGroups(res.Findings, hosts)
	res.Hosts, res.Counts = scoreHosts(disc.Hosts, res.Findings)
	res.ByBiz = aggregate("biz", res.Hosts)
	res.ByEnv = aggregate("env", res.Hosts)
	reboots, rebootQueried := rebootCounts(ctx, src, itemByID, hosts, opts)
	if rebootQueried {
		res.Queries++
	}
	res.Top = buildTop(values, itemByID, hosts, res.Findings, reboots, opts)
	return res, nil
}

func failedRun(it config.Item, idx int, err error) ItemRun {
	return ItemRun{ID: it.ID, Name: it.Name, QueryID: queryID(idx), PromQL: it.Query, Error: err.Error()}
}

func queryID(idx int) string { return fmt.Sprintf("Q%d", idx+1) }

func evalItem(ctx context.Context, src Source, it config.Item, idx int, overrides []config.Override, hosts map[string]*discovery.Host, opts Options) itemOutcome {
	run := ItemRun{ID: it.ID, Name: it.Name, QueryID: queryID(idx), PromQL: it.Query}
	if err := CheckWindows(it.Query, opts.MaxRange, opts.MinStep); err != nil {
		run.Error = err.Error()
		return itemOutcome{run: run}
	}
	start := time.Now()
	val, _, err := src.Query(ctx, it.Query, opts.Now)
	run.Duration = time.Since(start)
	if err != nil {
		run.Error = err.Error()
		return itemOutcome{run: run, queried: true}
	}
	vec, ok := val.(model.Vector)
	if !ok {
		run.Error = fmt.Sprintf("查询未返回瞬时向量: %T (expected instant vector)", val)
		return itemOutcome{run: run, queried: true}
	}
	run.Series = len(vec)

	sort.Slice(vec, func(a, b int) bool { return vec[a].Metric.String() < vec[b].Metric.String() })
	oc := itemOutcome{values: map[string]*hostValue{}, queried: true}
	evaluated := map[string]bool{}
	for _, s := range vec {
		v := float64(s.Value)
		if !usable(v) {
			continue
		}
		host := hosts[discovery.HostKey(string(s.Metric["instance"]))]
		if host == nil {
			oc.unmap++
			continue
		}
		if len(it.AppliesTo) > 0 && !hasAny(host, it.AppliesTo) {
			continue
		}
		evaluated[host.Key] = true
		warn, crit, over := EffectiveThreshold(it, overrides, host.Labels)
		level, crossed := Level(it.Compare, v, warn, crit)
		vk := host.Key
		for _, k := range it.GroupByExtra {
			vk += "\x00" + string(s.Metric[model.LabelName(k)])
		}
		cur := oc.values[vk]
		if cur == nil {
			cur = &hostValue{hostKey: host.Key, level: LevelOK}
			oc.values[vk] = cur
		}
		if level != LevelOK {
			cur.breaching++
		}
		if !better(it.Compare, level, v, cur) {
			continue
		}
		cur.value, cur.level, cur.crossed = v, level, crossed
		cur.warn, cur.crit, cur.overridden = warn, crit, over
		cur.labels = seriesLabels(s.Metric)
	}
	run.Hosts = len(evaluated)
	oc.run = run
	return oc
}

// better reports whether a sample with (level, v) should replace the kept one.
func better(op, level string, v float64, cur *hostValue) bool {
	if cur.labels == nil {
		return true
	}
	if rank(level) != rank(cur.level) {
		return rank(level) > rank(cur.level)
	}
	return moreExtreme(op, v, cur.value)
}

func rank(level string) int {
	switch level {
	case LevelCrit:
		return 2
	case LevelWarn:
		return 1
	}
	return 0
}

func hasAny(h *discovery.Host, ids []string) bool {
	for _, id := range ids {
		if h.HasService(id) {
			return true
		}
	}
	return false
}

func seriesLabels(m model.Metric) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		if k == model.MetricNameLabel {
			continue
		}
		out[string(k)] = string(v)
	}
	return out
}

func buildFinding(it config.Item, run ItemRun, host *discovery.Host, hv *hostValue) *Finding {
	extra := map[string]string{}
	for _, k := range it.GroupByExtra {
		if v, ok := hv.labels[k]; ok {
			extra[k] = v
		}
	}
	if len(extra) == 0 {
		extra = nil
	}
	return &Finding{
		CheckID: it.ID, CheckName: it.Name,
		HostKey: host.Key, Host: host.Name,
		Level: hv.level, Severity: effectiveSeverity(it.Severity, hv.level),
		Value: hv.value, Unit: it.Unit, Extra: extra,
		Breaching: hv.breaching, AdviceRef: it.AdviceRef,
		Evidence: Evidence{
			QueryID: run.QueryID, PromQL: it.Query, Value: hv.value, Labels: hv.labels,
			Threshold: Threshold{Compare: it.Compare, Warn: hv.warn, Crit: hv.crit, Crossed: hv.crossed, Overridden: hv.overridden},
		},
	}
}

var severityOrder = []string{"low", "medium", "high", "critical"}

// SeverityRank orders severities: low=0 .. critical=3.
func SeverityRank(s string) int {
	for i, v := range severityOrder {
		if v == s {
			return i
		}
	}
	return 0
}

// effectiveSeverity lowers the item severity one step for warn findings.
func effectiveSeverity(sev, level string) string {
	if level != LevelWarn {
		return sev
	}
	if r := SeverityRank(sev); r > 0 {
		return severityOrder[r-1]
	}
	return sev
}

func sortFindings(fs []*Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if ra, rb := SeverityRank(a.Severity), SeverityRank(b.Severity); ra != rb {
			return ra > rb
		}
		if a.CheckID != b.CheckID {
			return a.CheckID < b.CheckID
		}
		if a.Level != b.Level {
			return rank(a.Level) > rank(b.Level)
		}
		return findingLess(a, b)
	})
}

// findingLess orders by host name, then by job so per-job findings are stable.
func findingLess(a, b *Finding) bool {
	if a.Host != b.Host {
		return a.Host < b.Host
	}
	return a.Evidence.Labels["job"] < b.Evidence.Labels["job"]
}
