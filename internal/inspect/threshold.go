package inspect

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/prometheus/common/model"

	"yunwei/internal/config"
)

// Compare applies a comparison operator: value <op> threshold.
func Compare(op string, value, threshold float64) bool {
	switch op {
	case ">":
		return value > threshold
	case ">=":
		return value >= threshold
	case "<":
		return value < threshold
	case "<=":
		return value <= threshold
	case "==":
		return value == threshold
	case "!=":
		return value != threshold
	}
	return false
}

// EffectiveThreshold returns the item's warn/crit for a host after applying
// every matching override in file order, so later overrides win. overridden
// reports whether any override changed a value.
func EffectiveThreshold(it config.Item, overrides []config.Override, labels map[string]string) (warn, crit *float64, overridden bool) {
	warn, crit = it.Warn, it.Crit
	for _, o := range overrides {
		if !matches(o.Match, labels) {
			continue
		}
		ov, ok := o.Items[it.ID]
		if !ok {
			continue
		}
		if ov.Warn != nil {
			warn, overridden = ov.Warn, true
		}
		if ov.Crit != nil {
			crit, overridden = ov.Crit, true
		}
	}
	return warn, crit, overridden
}

func matches(match, labels map[string]string) bool {
	for k, v := range match {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// Level classifies value: crit is checked first, then warn.
func Level(op string, value float64, warn, crit *float64) (level string, crossed float64) {
	if crit != nil && Compare(op, value, *crit) {
		return LevelCrit, *crit
	}
	if warn != nil && Compare(op, value, *warn) {
		return LevelWarn, *warn
	}
	return LevelOK, 0
}

// moreExtreme reports whether a is further past the threshold direction than b.
// For operators without a direction the earlier sample is kept.
func moreExtreme(op string, a, b float64) bool {
	switch op {
	case ">", ">=":
		return a > b
	case "<", "<=":
		return a < b
	}
	return false
}

func usable(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// CheckWindows rejects queries whose range or subquery windows exceed maxRange
// or whose subquery step is below minStep.
func CheckWindows(query string, maxRange, minStep time.Duration) error {
	for _, tok := range bracketTokens(query) {
		window, step, hasStep := splitWindow(tok)
		w, err := model.ParseDuration(window)
		if err != nil {
			continue // not a duration, e.g. a regex character class
		}
		if time.Duration(w) > maxRange {
			return fmt.Errorf("查询窗口 [%s] 超过上限 %s (window exceeds max_range)", tok, maxRange)
		}
		if hasStep && step != "" {
			s, err := model.ParseDuration(step)
			if err != nil {
				continue
			}
			if time.Duration(s) < minStep {
				return fmt.Errorf("子查询步长 [%s] 小于下限 %s (step below min_step)", tok, minStep)
			}
		}
	}
	return nil
}

func bracketTokens(q string) []string {
	var out []string
	for {
		i := strings.IndexByte(q, '[')
		if i < 0 {
			return out
		}
		j := strings.IndexByte(q[i:], ']')
		if j < 0 {
			return out
		}
		out = append(out, q[i+1:i+j])
		q = q[i+j+1:]
	}
}

func splitWindow(tok string) (window, step string, hasStep bool) {
	window, step, hasStep = strings.Cut(tok, ":")
	return strings.TrimSpace(window), strings.TrimSpace(step), hasStep
}
