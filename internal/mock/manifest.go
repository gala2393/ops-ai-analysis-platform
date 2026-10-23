package mock

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// Anomaly is one planted problem the inspection is expected to find.
type Anomaly struct {
	CheckID string `json:"check_id"`
	Host    string `json:"host"`
	Level   string `json:"level"`
}

// DownEntry describes one host with unreachable targets.
type DownEntry struct {
	Host  string `json:"host"`
	Cause string `json:"cause"`
}

// Manifest is the ground truth written by -expected-out.
type Manifest struct {
	Hosts             int                 `json:"hosts"`
	Anomalies         []Anomaly           `json:"anomalies"`
	Services          map[string][]string `json:"services"`
	Down              []DownEntry         `json:"down"`
	UnclassifiedJobs  []string            `json:"unclassified_jobs"`
	MissingLabelHosts []string            `json:"missing_label_hosts"`
}

// Manifest derives the expected-results manifest from the fleet.
func (f *Fleet) Manifest() *Manifest {
	m := &Manifest{
		Hosts:             len(f.Hosts),
		Anomalies:         []Anomaly{},
		Services:          map[string][]string{},
		Down:              []DownEntry{},
		UnclassifiedJobs:  []string{},
		MissingLabelHosts: f.MissingLabelHosts(),
	}
	if m.MissingLabelHosts == nil {
		m.MissingLabelHosts = []string{}
	}
	alive := map[string]bool{}
	for _, h := range f.Hosts {
		alive[h.Name] = true
	}
	for _, a := range f.Planted {
		if alive[a.Host] {
			m.Anomalies = append(m.Anomalies, a)
		}
	}
	sort.Slice(m.Anomalies, func(i, j int) bool {
		a, b := m.Anomalies[i], m.Anomalies[j]
		if a.CheckID != b.CheckID {
			return a.CheckID < b.CheckID
		}
		return a.Host < b.Host
	})
	for _, h := range f.Hosts {
		m.Services[h.Name] = h.Services()
		down := 0
		for _, t := range h.Targets {
			if t.Down {
				down++
			}
		}
		switch {
		case down == len(h.Targets):
			m.Down = append(m.Down, DownEntry{h.Name, "host_down"})
		case down > 0:
			m.Down = append(m.Down, DownEntry{h.Name, "exporter_down"})
		}
		if h.Target(KindUnknown) != nil && len(m.UnclassifiedJobs) == 0 {
			m.UnclassifiedJobs = append(m.UnclassifiedJobs, f.UnknownJob)
		}
	}
	sort.Slice(m.Down, func(i, j int) bool { return m.Down[i].Host < m.Down[j].Host })
	return m
}

// WriteManifest writes the manifest as indented JSON, creating parent directories.
func (f *Fleet) WriteManifest(path string) error {
	b, err := json.MarshalIndent(f.Manifest(), "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
