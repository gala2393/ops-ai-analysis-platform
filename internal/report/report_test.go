package report

import (
	stdhtml "html"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yunwei/internal/config"
	"yunwei/internal/discovery"
	"yunwei/internal/inspect"
	"yunwei/internal/store"
)

const evil = "<script>alert(1)</script>"

const verbatimPromQL = "100 - (a / b * 100) # note `x`\n  + on() up{job=\"a  b\"} /* multi\nline */ < 1"

func sampleInput() Input {
	fin := time.Now()
	f := &inspect.Finding{CheckID: "disk_usage", CheckName: "磁盘使用率", HostKey: "10.0.0.1", Host: "app-" + evil,
		Level: inspect.LevelCrit, Severity: "high", Value: 93.256, Unit: "%", Extra: map[string]string{"mountpoint": "/data"},
		Evidence: inspect.Evidence{QueryID: "Q1", PromQL: verbatimPromQL, Value: 93.256, Labels: map[string]string{"job": "node", "instance": "10.0.0.1:9100"},
			Threshold: inspect.Threshold{Compare: ">", Crossed: 90}}}
	return Input{
		Run: &store.Run{ID: 7, Trigger: "cli", Status: "success", StartedAt: fin, FinishedAt: &fin,
			Hosts: store.HostCounts{Total: 3, OK: 1, Warn: 1, Crit: 1}, Groups: 1, Findings: 1},
		Findings: []*inspect.Finding{f},
		Groups: []*inspect.ProblemGroup{{CheckID: "disk_usage", CheckName: "磁盘使用率", Level: "crit", Severity: "high",
			Title: "磁盘使用率超过阈值（1 台）", Count: 1, HostNames: []string{f.Host}, Common: []inspect.Feature{{Field: "dc", Value: evil}}}},
		Detail: inspect.Detail{
			Items: []inspect.ItemRun{
				{ID: "disk_usage", Name: "磁盘使用率", Findings: 1},
				{ID: "bad", Name: "坏巡检项", PromQL: "nope(", Error: "bad_data: parse error\nline2"},
			},
			Hosts: []*inspect.HostHealth{
				{Key: "10.0.0.1", Name: f.Host, Labels: map[string]string{"dc": evil}, Score: 75, Status: "red", Crit: 1},
				{Key: "10.0.0.2", Name: "ok-1", Labels: map[string]string{"dc": "sh1"}, Score: 100, Status: "green"},
			},
			Top:          []inspect.TopList{{ID: "top_disk_usage", Title: "磁盘使用率最高", Unit: "%", Entries: []inspect.TopEntry{{Host: "h1", Value: 99.5}}}},
			Unclassified: []discovery.UnclassifiedJob{{Job: "weird", Targets: 2, Hosts: []string{"h1"}}},
			Diff:         discovery.NoPreviousDiff(),
		},
		Hosts: []*discovery.Host{
			{Key: "10.0.0.1", Name: f.Host, Status: discovery.StatusNormal,
				Services: []discovery.Service{{ID: "prometheus"}},
				Targets:  []discovery.Target{{Job: "prometheus", Instance: "10.0.0.1:9090", Up: true}}},
			{Key: "10.0.0.3", Name: "lab-1", MissingLabels: []string{"biz", "dc"}, Status: discovery.StatusHostDown,
				DownCause: discovery.CauseHostDown, DownError: "no route to host", DownJobs: []string{"node", "mysqld"},
				Targets: []discovery.Target{
					{Job: "node", Instance: "10.0.0.3:9100", LastError: "no route to host"},
					{Job: "mysqld", Instance: "10.0.0.3:9104", LastError: "connection refused"}}},
		},
		Items: []config.Item{{ID: "disk_usage", Name: "磁盘使用率", Unit: "%"}},
	}
}

func TestBuildAndMarkdown(t *testing.T) {
	r := Build(sampleInput())
	md := r.Markdown()
	order := []string{"## 一、总览", "## 二、问题组", "## 三、主机清单", "## 四、单机详情", "## 五、Top N", "## 六、缺标签主机", "## 七、资产变化", "## 八、未分类的 Job", "## 九、目标宕机与原因", "## 十、执行失败的巡检项"}
	pos := -1
	for _, h := range order {
		i := strings.Index(md, h)
		if i < 0 || i < pos {
			t.Fatalf("heading %q missing or out of order", h)
		}
		pos = i
	}
	for _, want := range []string{
		"`93.26%`", "`> 90%`", "mountpoint=/data", "job=node，instance=10.0.0.1:9100",
		"| 正常 | prometheus |", "prometheus / 10.0.0.1:9090 / 正常", "node / 10.0.0.3:9100 / 宕机",
		"job=node，instance=10.0.0.3:9100，lastError：no route to host", "job=mysqld，instance=10.0.0.3:9104，lastError：connection refused",
		"lab-1：缺少 biz, dc", "无上次巡检记录", "weird", "config/services.yaml", "主机宕机或网络不通", "no route to host",
		"坏巡检项", "执行失败", "bad_data: parse error line2", "通用默认（待企业标准替换）", "磁盘使用率最高", "h1：99.5%",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
	if strings.Contains(md, evil) {
		t.Error("raw <script> in markdown")
	}
}

func TestPromQLVerbatim(t *testing.T) {
	r := Build(sampleInput())
	if got := r.Hosts[0].Findings[0].PromQL; got != verbatimPromQL {
		t.Fatalf("model promql changed: %q", got)
	}
	md := r.Markdown()
	var block []string
	in := false
	for _, l := range strings.Split(md, "\n") {
		if strings.HasPrefix(l, "  ```promql") {
			in = true
			continue
		}
		if in && l == "  ```" {
			break
		}
		if in {
			block = append(block, strings.TrimPrefix(l, "  "))
		}
	}
	if got := strings.Join(block, "\n"); got != verbatimPromQL {
		t.Errorf("markdown block = %q, want %q", got, verbatimPromQL)
	}
	html, err := r.HTML()
	if err != nil {
		t.Fatal(err)
	}
	const open = `<pre class="promql"><code>`
	i := strings.Index(html, open)
	j := strings.Index(html[i+1:], "</code></pre>")
	if i < 0 || j < 0 || stdhtml.UnescapeString(html[i+len(open):i+1+j]) != verbatimPromQL {
		t.Errorf("html missing verbatim pre/code block")
	}
	if !strings.Contains(html, "white-space:pre-wrap") {
		t.Error("html pre lacks white-space preservation")
	}
}

func TestHTMLEscapes(t *testing.T) {
	html, err := Build(sampleInput()).HTML()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "<script>") {
		t.Error("unescaped <script> in HTML")
	}
	if !strings.Contains(html, "&lt;script&gt;alert(1)") {
		t.Error("escaped form not found")
	}
	for _, want := range []string{"总览", "问题组", "主机清单", "10.0.0.3:9104", "10.0.0.1:9100", "单机详情", "执行失败的巡检项", "通用默认（待企业标准替换）", "93.26%"} {
		if !strings.Contains(html, want) {
			t.Errorf("html missing %q", want)
		}
	}
}

func TestFailedItemKeepsOthers(t *testing.T) {
	r := Build(sampleInput())
	if len(r.FailedItems) != 1 || r.FailedItems[0].ID != "bad" {
		t.Fatalf("failed items = %+v", r.FailedItems)
	}
	if len(r.Hosts) != 1 || len(r.Hosts[0].Findings) != 1 {
		t.Fatalf("hosts = %+v", r.Hosts)
	}
}

func TestOptionalSections(t *testing.T) {
	in := sampleInput()
	r := Build(in)
	r.Banner = &Banner{Level: "warn", Text: "本报告未包含 AI 分析"}
	r.Groups[0].AI = &AISection{Phenomenon: "磁盘满", Causes: []string{evil}}
	md := r.Markdown()
	html, _ := r.HTML()
	if !strings.Contains(md, "本报告未包含 AI 分析") || !strings.Contains(html, "AI 分析") {
		t.Error("banner/AI section not rendered")
	}
	if strings.Contains(html, evil) {
		t.Error("AI text not escaped")
	}
}

func TestWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	f, err := Write(dir, Build(sampleInput()))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(f.Markdown) != "7.md" || filepath.Base(f.HTML) != "7.html" {
		t.Fatalf("files = %+v", f)
	}
	for _, p := range []string{f.Markdown, f.HTML} {
		if fi, err := os.Stat(p); err != nil || fi.Size() == 0 {
			t.Errorf("%s: %v", p, err)
		}
	}
}
