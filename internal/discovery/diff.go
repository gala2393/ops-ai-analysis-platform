package discovery

import "sort"

// HostRef names a host in an asset diff.
type HostRef struct {
	Key      string   `json:"key"`
	Name     string   `json:"name"`
	Services []string `json:"services,omitempty"`
}

// ServiceChange lists services gained or lost by one host.
type ServiceChange struct {
	Key      string   `json:"key"`
	Name     string   `json:"name"`
	Services []string `json:"services"`
}

// AssetDiff compares the host inventory with the previous run's. When
// HasPrevious is false there was no earlier run: nothing is reported as
// added or removed.
type AssetDiff struct {
	HasPrevious     bool            `json:"has_previous"`
	PreviousRunID   int64           `json:"previous_run_id,omitempty"`
	AddedHosts      []HostRef       `json:"added_hosts"`
	RemovedHosts    []HostRef       `json:"removed_hosts"`
	ServicesAdded   []ServiceChange `json:"services_added"`
	ServicesRemoved []ServiceChange `json:"services_removed"`
}

// Empty reports whether nothing changed.
func (d *AssetDiff) Empty() bool {
	return len(d.AddedHosts) == 0 && len(d.RemovedHosts) == 0 &&
		len(d.ServicesAdded) == 0 && len(d.ServicesRemoved) == 0
}

// NoPreviousDiff is the diff of a first run.
func NoPreviousDiff() *AssetDiff {
	return &AssetDiff{AddedHosts: []HostRef{}, RemovedHosts: []HostRef{}, ServicesAdded: []ServiceChange{}, ServicesRemoved: []ServiceChange{}}
}

// Diff compares previous and current inventories by host key. Lists are
// sorted by host name.
func Diff(previous, current []*Host, previousRunID int64) *AssetDiff {
	d := NoPreviousDiff()
	d.HasPrevious = true
	d.PreviousRunID = previousRunID

	prev := map[string]*Host{}
	for _, h := range previous {
		prev[h.Key] = h
	}
	cur := map[string]*Host{}
	for _, h := range current {
		cur[h.Key] = h
	}
	for _, h := range current {
		p, ok := prev[h.Key]
		if !ok {
			d.AddedHosts = append(d.AddedHosts, HostRef{Key: h.Key, Name: h.Name, Services: h.ServiceIDs()})
			continue
		}
		if added := missingFrom(h.ServiceIDs(), p.ServiceIDs()); len(added) > 0 {
			d.ServicesAdded = append(d.ServicesAdded, ServiceChange{Key: h.Key, Name: h.Name, Services: added})
		}
		if removed := missingFrom(p.ServiceIDs(), h.ServiceIDs()); len(removed) > 0 {
			d.ServicesRemoved = append(d.ServicesRemoved, ServiceChange{Key: h.Key, Name: h.Name, Services: removed})
		}
	}
	for _, p := range previous {
		if _, ok := cur[p.Key]; !ok {
			d.RemovedHosts = append(d.RemovedHosts, HostRef{Key: p.Key, Name: p.Name, Services: p.ServiceIDs()})
		}
	}
	sort.Slice(d.AddedHosts, func(i, j int) bool { return d.AddedHosts[i].Name < d.AddedHosts[j].Name })
	sort.Slice(d.RemovedHosts, func(i, j int) bool { return d.RemovedHosts[i].Name < d.RemovedHosts[j].Name })
	sort.Slice(d.ServicesAdded, func(i, j int) bool { return d.ServicesAdded[i].Name < d.ServicesAdded[j].Name })
	sort.Slice(d.ServicesRemoved, func(i, j int) bool { return d.ServicesRemoved[i].Name < d.ServicesRemoved[j].Name })
	return d
}

// missingFrom returns the ids in a that are not in b, sorted.
func missingFrom(a, b []string) []string {
	in := map[string]bool{}
	for _, id := range b {
		in[id] = true
	}
	var out []string
	for _, id := range a {
		if !in[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}
