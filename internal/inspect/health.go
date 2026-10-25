package inspect

import (
	"math"
	"sort"

	"yunwei/internal/discovery"
)

// unlabeled names the aggregate bucket of hosts missing the biz/env label.
const unlabeled = "未标注"

// critDeduction is the score lost per crit finding by severity; warn findings lose half.
var critDeduction = map[string]float64{"critical": 40, "high": 25, "medium": 10, "low": 5}

// scoreHosts computes the health of every discovered host. A host that is
// down scores 0; otherwise points are deducted per finding.
func scoreHosts(hosts []*discovery.Host, findings []*Finding) ([]*HostHealth, HostCounts) {
	byHost := map[string][]*Finding{}
	for _, f := range findings {
		byHost[f.HostKey] = append(byHost[f.HostKey], f)
	}
	out := make([]*HostHealth, 0, len(hosts))
	var counts HostCounts
	for _, h := range hosts {
		hh := &HostHealth{Key: h.Key, Name: h.Name, Labels: h.Labels}
		score := 100.0
		for _, f := range byHost[h.Key] {
			d := critDeduction[f.Severity]
			if f.Level == LevelWarn {
				hh.Warn++
				d /= 2
			} else {
				hh.Crit++
			}
			score -= d
		}
		switch {
		case h.Status == discovery.StatusHostDown:
			hh.Score, hh.Status, hh.DownCause = 0, StatusRed, h.DownCause
		default:
			hh.Score = int(math.Round(math.Max(score, 0)))
			hh.DownCause = h.DownCause
			switch {
			case hh.Crit > 0:
				hh.Status = StatusRed
			case hh.Warn > 0 || !h.Normal():
				hh.Status = StatusYellow
			default:
				hh.Status = StatusGreen
			}
		}
		counts.Total++
		switch hh.Status {
		case StatusRed:
			counts.Crit++
		case StatusYellow:
			counts.Warn++
		default:
			counts.OK++
		}
		out = append(out, hh)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score < out[j].Score
		}
		return out[i].Name < out[j].Name
	})
	return out, counts
}

// AggregateBy summarises host health per value of the internal label field dim.
func AggregateBy(dim string, hosts []*HostHealth) []Aggregate { return aggregate(dim, hosts) }

// aggregate summarises health per value of the internal label field dim.
// A bucket is red when at least a fifth of its hosts are red.
func aggregate(dim string, hosts []*HostHealth) []Aggregate {
	byName := map[string]*Aggregate{}
	sums := map[string]int{}
	for _, h := range hosts {
		name := h.Labels[dim]
		if name == "" {
			name = unlabeled
		}
		a := byName[name]
		if a == nil {
			a = &Aggregate{Dimension: dim, Name: name}
			byName[name] = a
		}
		a.Hosts++
		sums[name] += h.Score
		switch h.Status {
		case StatusRed:
			a.Red++
		case StatusYellow:
			a.Yellow++
		default:
			a.Green++
		}
	}
	out := make([]Aggregate, 0, len(byName))
	for name, a := range byName {
		a.AvgScore = math.Round(float64(sums[name])/float64(a.Hosts)*10) / 10
		switch {
		case a.Red*5 >= a.Hosts:
			a.Status = StatusRed
		case a.Red+a.Yellow > 0:
			a.Status = StatusYellow
		default:
			a.Status = StatusGreen
		}
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AvgScore != out[j].AvgScore {
			return out[i].AvgScore < out[j].AvgScore
		}
		return out[i].Name < out[j].Name
	})
	return out
}
