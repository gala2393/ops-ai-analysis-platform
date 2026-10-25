package discovery

import (
	"reflect"
	"testing"
)

func host(key string, services ...string) *Host {
	h := &Host{Key: key, Name: "n-" + key}
	for _, s := range services {
		h.Services = append(h.Services, Service{ID: s})
	}
	return h
}

func TestDiff(t *testing.T) {
	prev := []*Host{host("a", "node", "mysql"), host("b", "node"), host("gone", "node")}
	cur := []*Host{host("a", "node"), host("b", "node", "mysql"), host("new1", "node"), host("new2", "node", "redis")}

	d := Diff(prev, cur, 7)
	if !d.HasPrevious || d.PreviousRunID != 7 {
		t.Errorf("previous: %+v", d)
	}
	names := func(r []HostRef) (out []string) {
		for _, h := range r {
			out = append(out, h.Key)
		}
		return
	}
	if got := names(d.AddedHosts); !reflect.DeepEqual(got, []string{"new1", "new2"}) {
		t.Errorf("added = %v", got)
	}
	if got := names(d.RemovedHosts); !reflect.DeepEqual(got, []string{"gone"}) {
		t.Errorf("removed = %v", got)
	}
	if len(d.ServicesAdded) != 1 || d.ServicesAdded[0].Key != "b" || !reflect.DeepEqual(d.ServicesAdded[0].Services, []string{"mysql"}) {
		t.Errorf("services added = %+v", d.ServicesAdded)
	}
	if len(d.ServicesRemoved) != 1 || d.ServicesRemoved[0].Key != "a" || !reflect.DeepEqual(d.ServicesRemoved[0].Services, []string{"mysql"}) {
		t.Errorf("services removed = %+v", d.ServicesRemoved)
	}
	if d.Empty() {
		t.Error("diff reported empty")
	}
}

func TestNoPreviousDiffReportsNothingRemoved(t *testing.T) {
	d := NoPreviousDiff()
	if d.HasPrevious || !d.Empty() || d.RemovedHosts == nil {
		t.Errorf("first-run diff: %+v", d)
	}
}
