package web

import (
	"context"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"

	"yunwei/internal/discovery"
)

// chartView is the model of the inline SVG trend chart.
type chartView struct {
	Title       string
	Width       int
	Height      int
	PlotX0      int
	PlotY0      int
	PlotX1      int
	PlotY1      int
	XLabelY     int
	YMaxLabel   string
	YMinLabel   string
	XStartLabel string
	XEndLabel   string
	Series      []chartSeries
	Unavailable bool
}

// chartSeries is one polyline of the chart.
type chartSeries struct {
	Name   string
	Points string
	Color  string
}

var chartColors = []string{"#2563eb", "#dc2626", "#16a34a", "#d97706", "#7c3aed", "#0891b2", "#be185d", "#4d7c0f"}

// diskTrend queries the host's per-mountpoint disk usage over the last 6
// hours (step 5m, clamped by the client's min_step) and turns it into an
// inline SVG model. Any failure degrades to an unavailable notice; the page
// itself must always render.
func (s *Server) diskTrend(ctx context.Context, h *discovery.Host) chartView {
	cv := chartView{
		Title: "磁盘使用率（%）", Width: 640, Height: 220,
		PlotX0: 56, PlotY0: 16, PlotX1: 628, PlotY1: 178, XLabelY: 200,
	}
	if s.prom == nil {
		cv.Unavailable = true
		return cv
	}
	match := `{instance=~"` + instanceRegexp(h.Key) + `:.*", fstype!~"tmpfs|overlay"}`
	query := `100 - (node_filesystem_avail_bytes` + match + ` / node_filesystem_size_bytes` + match + ` * 100)`
	end := time.Now()
	val, _, err := s.prom.QueryRange(ctx, query, promv1.Range{
		Start: end.Add(-6 * time.Hour), End: end, Step: 5 * time.Minute,
	})
	if err != nil {
		slog.Warn("趋势查询失败 (trend query failed)", "host", h.Key, "err", err)
		cv.Unavailable = true
		return cv
	}
	matrix, ok := val.(model.Matrix)
	if !ok || len(matrix) == 0 {
		cv.Unavailable = true
		return cv
	}

	streams := make([]*model.SampleStream, 0, len(matrix))
	for _, ss := range matrix {
		if len(ss.Values) > 1 {
			streams = append(streams, ss)
		}
	}
	if len(streams) == 0 {
		cv.Unavailable = true
		return cv
	}
	sort.Slice(streams, func(i, j int) bool { return seriesName(streams[i]) < seriesName(streams[j]) })
	if len(streams) > len(chartColors) {
		streams = streams[:len(chartColors)]
	}

	xMin, xMax := float64(streams[0].Values[0].Timestamp.Unix()), float64(streams[0].Values[len(streams[0].Values)-1].Timestamp.Unix())
	yMin, yMax := streams[0].Values[0].Value, streams[0].Values[0].Value
	for _, ss := range streams {
		for _, p := range ss.Values {
			if x := float64(p.Timestamp.Unix()); x < xMin {
				xMin = x
			}
			if x := float64(p.Timestamp.Unix()); x > xMax {
				xMax = x
			}
			if p.Value < yMin {
				yMin = p.Value
			}
			if p.Value > yMax {
				yMax = p.Value
			}
		}
	}
	// Pad the y range so the polylines do not touch the axes.
	span := float64(yMax - yMin)
	if span < 1 {
		span = 1
	}
	yMinF := float64(yMin) - span*0.05
	yMaxF := float64(yMax) + span*0.05
	if xMax <= xMin {
		xMax = xMin + 1
	}

	plotW := float64(cv.PlotX1 - cv.PlotX0)
	plotH := float64(cv.PlotY1 - cv.PlotY0)
	for i, ss := range streams {
		pts := ""
		for j, p := range ss.Values {
			x := float64(cv.PlotX0) + (float64(p.Timestamp.Unix())-xMin)/(xMax-xMin)*plotW
			y := float64(cv.PlotY1) - (float64(p.Value)-yMinF)/(yMaxF-yMinF)*plotH
			if j > 0 {
				pts += " "
			}
			pts += strconv.FormatFloat(x, 'f', 1, 64) + "," + strconv.FormatFloat(y, 'f', 1, 64)
		}
		cv.Series = append(cv.Series, chartSeries{
			Name:   seriesName(ss),
			Points: pts,
			Color:  chartColors[i%len(chartColors)],
		})
	}
	cv.YMaxLabel = strconv.FormatFloat(yMaxF, 'f', 1, 64) + "%"
	cv.YMinLabel = strconv.FormatFloat(yMinF, 'f', 1, 64) + "%"
	cv.XStartLabel = time.Unix(int64(xMin), 0).Format("15:04")
	cv.XEndLabel = time.Unix(int64(xMax), 0).Format("15:04")
	return cv
}

// instanceRegexp quotes a host key for an instance=~ matcher. PromQL string
// literals have no "." escape, so the regex escaping from QuoteMeta must be
// doubled in the query text.
func instanceRegexp(ip string) string {
	return strings.ReplaceAll(regexp.QuoteMeta(ip), `\`, `\\`)
}

// seriesName labels a trend line by mountpoint, falling back to instance.
func seriesName(ss *model.SampleStream) string {
	if mp := string(ss.Metric["mountpoint"]); mp != "" {
		return "挂载点 " + mp
	}
	return string(ss.Metric["instance"])
}
