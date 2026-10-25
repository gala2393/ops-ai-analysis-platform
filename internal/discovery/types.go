// Package discovery builds the host inventory from Prometheus: it merges
// targets into hosts, names them, maps enterprise labels, detects services and
// classifies why targets are down. It only issues read-only queries, and the
// number of queries depends on the number of service rules, never on hosts.
package discovery

import (
	"context"
	"time"

	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

// Source is the subset of the prom client discovery needs.
type Source interface {
	Query(ctx context.Context, query string, ts time.Time) (model.Value, promv1.Warnings, error)
	Targets(ctx context.Context) (promv1.TargetsResult, error)
}

// Detection sources of a service.
const (
	SourceJob     = "job"
	SourceApp     = "app"
	SourceMetric  = "metric"
	SourceSystemd = "systemd"
)

// Host status as far as discovery can tell. A host with any down target is
// never StatusNormal.
const (
	StatusNormal       = "normal"
	StatusExporterDown = "exporter_down"
	StatusHostDown     = "host_down"
)

// Down causes (Host.DownCause).
const (
	CauseHostDown     = "host_down"
	CauseExporterDown = "exporter_down"
)

// Chinese descriptions of the down causes.
const (
	CauseHostDownText     = "主机宕机或网络不通"
	CauseExporterDownText = "exporter 挂了"
)

// Internal label fields, in report order.
var LabelFields = []string{"env", "role", "biz", "dc", "owner"}

// Target is one scrape target of a host.
type Target struct {
	Job       string `json:"job"`
	App       string `json:"app,omitempty"`
	Instance  string `json:"instance"`
	Up        bool   `json:"up"`
	LastError string `json:"last_error,omitempty"`
}

// Service is a detected service with every kind of evidence that revealed it.
type Service struct {
	ID      string   `json:"id"`
	Sources []string `json:"sources"`
}

// Host is one machine, merged from all targets sharing an IP.
type Host struct {
	Key  string   `json:"key"`
	Name string   `json:"name"`
	IPs  []string `json:"ips"`
	// Labels holds mapped internal fields (env/role/biz/dc/owner) that are present.
	Labels        map[string]string `json:"labels"`
	MissingLabels []string          `json:"missing_labels"`
	Targets       []Target          `json:"targets"`
	Services      []Service         `json:"services"`
	Status        string            `json:"status"`
	DownCause     string            `json:"down_cause,omitempty"`
	DownText      string            `json:"down_text,omitempty"`
	// DownJobs names the jobs whose targets are down.
	DownJobs []string `json:"down_jobs,omitempty"`
	// DownError is the lastError of the first down target.
	DownError string `json:"down_error,omitempty"`
	// NetworkHint is set when lastError looks like a network/host failure.
	NetworkHint bool `json:"network_hint,omitempty"`
}

// Normal reports whether the host has no down targets.
func (h *Host) Normal() bool { return h.Status == StatusNormal }

// ServiceIDs returns the detected service ids, sorted.
func (h *Host) ServiceIDs() []string {
	out := make([]string, len(h.Services))
	for i, s := range h.Services {
		out[i] = s.ID
	}
	return out
}

// HasService reports whether the service was detected on the host.
func (h *Host) HasService(id string) bool {
	for _, s := range h.Services {
		if s.ID == id {
			return true
		}
	}
	return false
}

// GroupValue returns the host's value for an internal label field. When the
// label is missing it falls back to the primary job (or the host key) and
// reports fallback=true, so hosts without labels still land in a group.
func (h *Host) GroupValue(field string) (value string, fallback bool) {
	if v := h.Labels[field]; v != "" {
		return v, false
	}
	if len(h.Targets) > 0 && h.Targets[0].Job != "" {
		return h.Targets[0].Job, true
	}
	return h.Key, true
}

// UnclassifiedJob is a job no service rule recognises.
type UnclassifiedJob struct {
	Job     string   `json:"job"`
	Targets int      `json:"targets"`
	Hosts   []string `json:"hosts"`
}

// Result is the outcome of one discovery pass.
type Result struct {
	Hosts        []*Host           `json:"hosts"`
	Unclassified []UnclassifiedJob `json:"unclassified_jobs"`
	// Warnings lists non-fatal problems, e.g. a failed detection query.
	Warnings []string `json:"warnings,omitempty"`
	// Queries is the number of Prometheus requests discovery issued.
	Queries int `json:"queries"`
}

// Host returns the host with the given key, or nil.
func (r *Result) Host(key string) *Host {
	for _, h := range r.Hosts {
		if h.Key == key {
			return h
		}
	}
	return nil
}

// MissingLabelHosts returns hosts lacking at least one mapped label.
func (r *Result) MissingLabelHosts() []*Host {
	var out []*Host
	for _, h := range r.Hosts {
		if len(h.MissingLabels) > 0 {
			out = append(out, h)
		}
	}
	return out
}
