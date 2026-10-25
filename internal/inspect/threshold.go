package inspect

import (
	"fmt"
	"math"
	"time"

	"github.com/prometheus/prometheus/promql/parser"

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
// or whose subquery step is below minStep. It walks the parsed expression so
// every spelling the PromQL parser accepts (including bare seconds such as
// [172800]) is measured; a query that does not parse cannot be bounded and is
// rejected too.
func CheckWindows(query string, maxRange, minStep time.Duration) error {
	expr, err := parser.ParseExpr(query)
	if err != nil {
		return fmt.Errorf("PromQL 解析失败，无法校验查询窗口 (parse error): %v", err)
	}
	var werr error
	parser.Inspect(expr, func(n parser.Node, _ []parser.Node) error {
		switch v := n.(type) {
		case *parser.MatrixSelector:
			if v.Range > maxRange {
				werr = fmt.Errorf("查询窗口 [%s] 超过上限 %s (window exceeds max_range)", v.Range, maxRange)
			}
		case *parser.SubqueryExpr:
			switch {
			case v.Range > maxRange:
				werr = fmt.Errorf("子查询窗口 [%s] 超过上限 %s (window exceeds max_range)", v.Range, maxRange)
			case v.Step != 0 && v.Step < minStep:
				werr = fmt.Errorf("子查询步长 [%s] 小于下限 %s (step below min_step)", v.Step, minStep)
			}
		}
		if werr != nil {
			return werr
		}
		return nil
	})
	return werr
}
