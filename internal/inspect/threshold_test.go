package inspect

import (
	"strings"
	"testing"
	"time"

	"yunwei/internal/config"
)

func f64(v float64) *float64 { return &v }

func TestCompareOperators(t *testing.T) {
	cases := []struct {
		op        string
		v, thr    float64
		wantTrue  bool
		wantLabel string
	}{
		{">", 5, 5, false, "equal"}, {">", 6, 5, true, "above"}, {">", 4, 5, false, "below"},
		{">=", 5, 5, true, "equal"}, {">=", 4, 5, false, "below"}, {">=", 6, 5, true, "above"},
		{"<", 5, 5, false, "equal"}, {"<", 4, 5, true, "below"}, {"<", 6, 5, false, "above"},
		{"<=", 5, 5, true, "equal"}, {"<=", 6, 5, false, "above"}, {"<=", 4, 5, true, "below"},
		{"==", 5, 5, true, "equal"}, {"==", 4, 5, false, "below"},
		{"!=", 5, 5, false, "equal"}, {"!=", 4, 5, true, "below"},
		{"~", 5, 5, false, "unknown op"},
	}
	for _, c := range cases {
		if got := Compare(c.op, c.v, c.thr); got != c.wantTrue {
			t.Errorf("%v %s %v (%s) = %v, want %v", c.v, c.op, c.thr, c.wantLabel, got, c.wantTrue)
		}
	}
}

func TestLevelCritBeforeWarn(t *testing.T) {
	cases := []struct {
		op      string
		v       float64
		warn    *float64
		crit    *float64
		want    string
		crossed float64
	}{
		{">", 95, f64(80), f64(90), LevelCrit, 90},
		{">", 85, f64(80), f64(90), LevelWarn, 80},
		{">", 70, f64(80), f64(90), LevelOK, 0},
		{"<", 2, f64(7), f64(3), LevelCrit, 3},
		{"<", 5, f64(7), f64(3), LevelWarn, 7},
		{"<", 9, f64(7), f64(3), LevelOK, 0},
		{"==", 0, nil, f64(0), LevelCrit, 0},
		{"==", 1, nil, f64(0), LevelOK, 0},
		{"<", 10, f64(24), nil, LevelWarn, 24},
	}
	for _, c := range cases {
		got, crossed := Level(c.op, c.v, c.warn, c.crit)
		if got != c.want || crossed != c.crossed {
			t.Errorf("Level(%s,%v) = %s/%v, want %s/%v", c.op, c.v, got, crossed, c.want, c.crossed)
		}
	}
}

func TestOverridePrecedence(t *testing.T) {
	item := config.Item{ID: "disk_usage", Warn: f64(80), Crit: f64(90)}
	ov := func(match map[string]string, w, c *float64) config.Override {
		return config.Override{Match: match, Items: map[string]config.ThresholdOverride{"disk_usage": {Warn: w, Crit: c}}}
	}
	overrides := []config.Override{
		ov(map[string]string{"env": "prod"}, f64(70), f64(88)),
		ov(map[string]string{"role": "db"}, f64(75), nil),
		ov(map[string]string{"role": "db", "env": "staging"}, nil, f64(60)),
		{Match: map[string]string{"role": "db"}, Items: map[string]config.ThresholdOverride{"other": {Warn: f64(1)}}},
	}
	cases := []struct {
		name       string
		labels     map[string]string
		warn, crit float64
		overridden bool
	}{
		{"no match uses defaults", map[string]string{"env": "dev", "role": "app"}, 80, 90, false},
		{"single override", map[string]string{"env": "prod", "role": "app"}, 70, 88, true},
		{"later override wins on warn, earlier crit kept", map[string]string{"env": "prod", "role": "db"}, 75, 88, true},
		{"staging db picks the third override crit", map[string]string{"env": "staging", "role": "db"}, 75, 60, true},
		{"override for another item is ignored", map[string]string{"env": "dev", "role": "db"}, 75, 90, true},
		{"missing label never matches", map[string]string{"role": "app"}, 80, 90, false},
	}
	for _, c := range cases {
		w, cr, over := EffectiveThreshold(item, overrides, c.labels)
		if *w != c.warn || *cr != c.crit || over != c.overridden {
			t.Errorf("%s: got %v/%v over=%v, want %v/%v over=%v", c.name, *w, *cr, over, c.warn, c.crit, c.overridden)
		}
	}
	// The source item must not be mutated.
	if *item.Warn != 80 || *item.Crit != 90 {
		t.Errorf("item mutated: %v/%v", *item.Warn, *item.Crit)
	}
}

func TestCheckWindows(t *testing.T) {
	cases := []struct {
		q       string
		wantErr string
	}{
		{`rate(x[5m])`, ""},
		{`max_over_time((y)[1h:5m])`, ""},
		{`increase(x[24h])`, ""},
		{`increase(x[25h])`, "max_range"},
		{`increase(x[2d])`, "max_range"},
		{`max_over_time(y[1h:10s])`, "min_step"},
		{`up{job=~"a[0-9]+"}`, ""},
		{`increase(x[172800])`, "max_range"},
		{`increase(x[86400])`, ""},
		{`max_over_time(y[3600:1])`, "min_step"},
		{`max_over_time(y[172800:60])`, "max_range"},
		{`max_over_time(y[1h:1m])`, ""},
		{`max_over_time(rate(x[48h])[1h:5m])`, "max_range"},
		{`max_over_time(rate(x[5m])[2d:5m])`, "max_range"},
		{`up == 1 and increase(x[2d]) > 0`, "max_range"},
		{`increase(x[`, "parse"},
	}
	for _, c := range cases {
		err := CheckWindows(c.q, 24*time.Hour, time.Minute)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%q: unexpected error %v", c.q, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%q: error = %v, want containing %q", c.q, err, c.wantErr)
		}
	}
}

func TestDefaultItemsAreBounded(t *testing.T) {
	in, err := config.LoadInspection("../../config/inspection.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(in.Items); n < 10 {
		t.Errorf("default items = %d, want >= 10", n)
	}
	for _, it := range in.Items {
		if err := CheckWindows(it.Query, 24*time.Hour, time.Minute); err != nil {
			t.Errorf("item %s: %v", it.ID, err)
		}
		if it.Source != config.DefaultSource {
			t.Errorf("item %s source = %q", it.ID, it.Source)
		}
	}
}
