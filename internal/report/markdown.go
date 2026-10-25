package report

import (
	"fmt"
	"strings"
)

// mdEscape neutralises Markdown and raw-HTML syntax in data values.
func mdEscape(s string) string {
	s = oneLine(s)
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\', '`', '*', '|', '<', '>', '[', ']':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// mdCode wraps s in a code span long enough to survive embedded backticks.
func mdCode(s string) string {
	s = oneLine(s)
	fence := "`"
	for strings.Contains(s, fence) {
		fence += "`"
	}
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " "
	}
	return fence + s + fence
}

// mdBlock renders s verbatim as a fenced code block nested two spaces deep
// (list-item content). The fence outgrows any backtick run inside s.
func mdBlock(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	var b strings.Builder
	b.WriteString("  " + fence + "promql\n")
	for _, line := range strings.Split(s, "\n") {
		b.WriteString("  " + line + "\n")
	}
	b.WriteString("  " + fence)
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Markdown renders the report.
func (r *Report) Markdown() string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	w("# %s", mdEscape(r.Title))
	w("")
	w("- 开始时间：%s", r.StartedAt)
	if r.FinishedAt != "" {
		w("- 结束时间：%s", r.FinishedAt)
	}
	w("- 触发方式：%s　状态：%s　耗时：%s", mdEscape(r.Trigger), mdEscape(r.RunStatus), r.Duration)
	w("- 报告生成：%s", r.GeneratedAt)
	if r.Banner != nil {
		w("")
		w("> **%s**", mdEscape(r.Banner.Text))
	}
	if len(r.FailedItems) > 0 {
		w("")
		w("> **注意：有 %d 个巡检项执行失败，相关结论不完整，详见“执行失败的巡检项”。**", len(r.FailedItems))
	}
	if r.SourceNote != "" {
		w("")
		w("> %s", mdEscape(r.SourceNote))
	}

	ov := r.Overview
	w("")
	w("## 一、总览")
	w("")
	w("- 服务器总数：%d，正常：%d，关注：%d，异常：%d", ov.Total, ov.Normal, ov.Warn, ov.Crit)
	w("- 问题分组：%d，发现项：%d，执行失败巡检项：%d", ov.GroupCount, ov.FindingCount, ov.FailedItemCount)
	w("- Prometheus 查询：%d 次，总耗时 %d ms，P95 %d ms", ov.QueryCount, ov.QueryTotalMS, ov.QueryP95MS)
	mdAgg(&b, "按业务（biz）", ov.ByBiz)
	mdAgg(&b, "按机房（dc）", ov.ByDC)
	mdAgg(&b, "按环境（env）", ov.ByEnv)

	w("")
	w("## 二、问题组")
	if len(r.Groups) == 0 {
		w("")
		w("本次巡检未发现问题。")
	}
	for i, g := range r.Groups {
		w("")
		w("### %d. %s", i+1, mdEscape(g.Title))
		w("")
		w("- 巡检项：%s　级别：%s　严重度：%s　影响主机：%d 台", mdEscape(g.CheckName), g.LevelText, mdEscape(g.Severity), g.Count)
		if len(g.Common) > 0 {
			w("- 共同特征：%s", mdEscape(strings.Join(g.Common, "，")))
		}
		if g.Advice != "" {
			w("- 处置参考：%s", mdEscape(g.Advice))
		}
		w("- 受影响主机：%s", mdEscape(strings.Join(g.Hosts, "，")))
		if g.AI != nil {
			mdAI(&b, g.AI)
		}
	}

	w("")
	w("## 三、主机清单")
	w("")
	if len(r.Inventory) == 0 {
		w("没有发现主机。")
	} else {
		w("| 主机 | 状态 | 已识别服务 | 采集目标（job / instance / 健康） |")
		w("|---|---|---|---|")
		for _, h := range r.Inventory {
			ts := make([]string, len(h.Targets))
			for i, t := range h.Targets {
				ts[i] = fmt.Sprintf("%s / %s / %s", t.Job, t.Instance, t.Health)
			}
			w("| %s | %s | %s | %s |", mdEscape(h.Name), h.StatusText, mdEscape(orDash(h.Services)), mdEscape(orDash(strings.Join(ts, "；"))))
		}
	}

	w("")
	w("## 四、单机详情")
	if len(r.Hosts) == 0 {
		w("")
		w("没有存在发现项的主机。")
	}
	for _, h := range r.Hosts {
		w("")
		w("### %s（%s，健康分 %d）", mdEscape(h.Name), h.StatusText, h.Score)
		w("")
		if h.Labels != "" {
			w("- 标签：%s", mdEscape(h.Labels))
		}
		if h.DownText != "" {
			w("- 状态：%s", mdEscape(h.DownText))
		}
		for _, f := range h.Findings {
			line := fmt.Sprintf("- [%s] %s", f.LevelText, mdEscape(f.CheckName))
			if f.Job != "" || f.Instance != "" {
				line += fmt.Sprintf("（job=%s，instance=%s）", mdEscape(orDash(f.Job)), mdEscape(orDash(f.Instance)))
			}
			line += fmt.Sprintf("：当前值 %s，阈值 %s", mdCode(f.Value), mdCode(f.Threshold))
			if f.Extra != "" {
				line += "，" + mdEscape(f.Extra)
			}
			line += fmt.Sprintf("，PromQL（%s）：", mdEscape(f.QueryID))
			w("%s", line)
			w("%s", mdBlock(f.PromQL))
		}
	}

	w("")
	w("## 五、Top N")
	for _, t := range r.Top {
		w("")
		w("### %s", mdEscape(t.Title))
		w("")
		if len(t.Entries) == 0 {
			w("暂无数据。")
			continue
		}
		for _, e := range t.Entries {
			extra := ""
			if e.Extra != "" {
				extra = "（" + mdEscape(e.Extra) + "）"
			}
			w("%d. %s：%s%s", e.Rank, mdEscape(e.Host), mdEscape(e.Value), extra)
		}
	}

	w("")
	w("## 六、缺标签主机")
	w("")
	if len(r.MissingLabels) == 0 {
		w("所有主机标签完整。")
	} else {
		w("以下主机缺少企业标签（env/role/biz/dc/owner），会影响分组与归属：")
		w("")
		for _, m := range r.MissingLabels {
			w("- %s：缺少 %s", mdEscape(m.Host), mdEscape(m.Missing))
		}
	}

	w("")
	w("## 七、资产变化")
	w("")
	w("%s", mdEscape(r.Assets.Summary))
	if len(r.Assets.Lines) > 0 {
		w("")
		for _, l := range r.Assets.Lines {
			w("- %s", mdEscape(l))
		}
	}

	w("")
	w("## 八、未分类的 Job")
	w("")
	if len(r.Unclassified) == 0 {
		w("所有 job 均已被服务规则识别。")
	} else {
		w("以下 job 未匹配任何服务规则（未分类），请在 config/services.yaml 中补充规则：")
		w("")
		for _, u := range r.Unclassified {
			w("- %s：%d 个目标，主机 %s", mdEscape(u.Job), u.Targets, mdEscape(orDash(u.Hosts)))
		}
	}

	w("")
	w("## 九、目标宕机与原因")
	w("")
	if len(r.Down) == 0 {
		w("所有采集目标正常。")
	} else {
		for _, d := range r.Down {
			w("- %s：%s", mdEscape(d.Host), mdEscape(d.Cause))
			for _, t := range d.Targets {
				w("  - job=%s，instance=%s，lastError：%s", mdEscape(orDash(t.Job)), mdEscape(orDash(t.Instance)), mdEscape(orDash(t.LastError)))
			}
		}
	}

	if len(r.FailedItems) > 0 {
		w("")
		w("## 十、执行失败的巡检项")
		w("")
		for _, f := range r.FailedItems {
			w("- %s（%s）：执行失败，错误：%s，PromQL：", mdEscape(f.Name), mdEscape(f.ID), mdEscape(f.Error))
			w("%s", mdBlock(f.PromQL))
		}
	}

	w("")
	w("## 附录：巡检项与来源")
	w("")
	w("%s", mdEscape(r.SourceNote))
	w("")
	for _, it := range r.Items {
		w("- %s（%s）：%s，发现 %d 项，来源：%s", mdEscape(it.Name), mdEscape(it.ID), it.Result, it.Findings, mdEscape(it.Source))
	}
	if len(r.Warnings) > 0 {
		w("")
		w("### 告警信息")
		w("")
		for _, x := range r.Warnings {
			w("- %s", mdEscape(x))
		}
	}
	return b.String()
}

func mdAgg(b *strings.Builder, title string, rows []AggRow) {
	fmt.Fprintf(b, "\n**%s**\n\n", title)
	if len(rows) == 0 {
		b.WriteString("暂无数据。\n")
		return
	}
	b.WriteString("| 名称 | 主机数 | 绿 | 黄 | 红 | 平均健康分 | 状态 |\n|---|---|---|---|---|---|---|\n")
	for _, a := range rows {
		fmt.Fprintf(b, "| %s | %d | %d | %d | %d | %s | %s |\n", mdEscape(a.Name), a.Hosts, a.Green, a.Yellow, a.Red, a.AvgScore, a.StatusText)
	}
}

func mdAI(b *strings.Builder, a *AISection) {
	fmt.Fprintf(b, "\n#### AI 分析\n\n")
	if a.Phenomenon != "" {
		fmt.Fprintf(b, "- 现象：%s\n", mdEscape(a.Phenomenon))
	}
	for _, c := range a.Causes {
		fmt.Fprintf(b, "- 可能原因：%s\n", mdEscape(c))
	}
	for _, s := range a.Steps {
		fmt.Fprintf(b, "- 排查步骤：%s\n", mdEscape(s))
	}
	if a.Impact != "" {
		fmt.Fprintf(b, "- 影响：%s\n", mdEscape(a.Impact))
	}
	for _, s := range a.Risks {
		fmt.Fprintf(b, "- 风险：%s\n", mdEscape(s))
	}
	if a.Note != "" {
		fmt.Fprintf(b, "- 校验：%s\n", mdEscape(a.Note))
	}
}
