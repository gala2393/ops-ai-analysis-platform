package report

import (
	"bytes"
	"html/template"
)

// HTML renders the report as a self-contained page. All data goes through
// html/template contextual escaping.
func (r *Report) HTML() (string, error) {
	var buf bytes.Buffer
	if err := htmlTmpl.Execute(&buf, r); err != nil {
		return "", err
	}
	return buf.String(), nil
}

var htmlTmpl = template.Must(template.New("report").Funcs(template.FuncMap{
	"join": func(s []string, sep string) string {
		var b bytes.Buffer
		for i, x := range s {
			if i > 0 {
				b.WriteString(sep)
			}
			b.WriteString(x)
		}
		return b.String()
	},
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	},
	"dash": orDash,
	"inc":  func(i int) int { return i + 1 },
}).Parse(htmlSource))

const htmlSource = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<title>{{.Title}}</title>
<style>
body{font-family:-apple-system,"Microsoft YaHei",sans-serif;max-width:1100px;margin:2em auto;padding:0 1em;color:#222;line-height:1.55}
h1{border-bottom:2px solid #444;padding-bottom:.3em}h2{margin-top:2em;border-bottom:1px solid #ccc;padding-bottom:.2em}
table{border-collapse:collapse;margin:.5em 0}td,th{border:1px solid #ccc;padding:4px 10px;text-align:left}th{background:#f3f3f3}
code{background:#f4f4f4;padding:1px 4px;border-radius:3px;word-break:break-all}
.banner{padding:.6em 1em;margin:1em 0;border-left:4px solid #888;background:#f7f7f7}
.banner.warn{border-color:#d9822b;background:#fff4e5}
.crit,.red{color:#c0392b;font-weight:bold}.warn,.yellow{color:#b7791f;font-weight:bold}.green{color:#2f855a;font-weight:bold}
.finding{margin:.3em 0}.meta{color:#555}
</style>
</head>
<body>
<h1>{{.Title}}</h1>
<ul class="meta">
<li>开始时间：{{.StartedAt}}</li>
{{if .FinishedAt}}<li>结束时间：{{.FinishedAt}}</li>{{end}}
<li>触发方式：{{.Trigger}}　状态：{{.RunStatus}}　耗时：{{.Duration}}</li>
<li>报告生成：{{.GeneratedAt}}</li>
</ul>
{{with .Banner}}<div class="banner {{.Level}}"><strong>{{.Text}}</strong></div>{{end}}
{{if .FailedItems}}<div class="banner warn"><strong>注意：有 {{len .FailedItems}} 个巡检项执行失败，相关结论不完整，详见“执行失败的巡检项”。</strong></div>{{end}}
{{if .SourceNote}}<div class="banner">{{.SourceNote}}</div>{{end}}

<h2 id="overview">一、总览</h2>
<ul>
<li>服务器总数：{{.Overview.Total}}，正常：{{.Overview.Normal}}，关注：{{.Overview.Warn}}，异常：{{.Overview.Crit}}</li>
<li>问题分组：{{.Overview.GroupCount}}，发现项：{{.Overview.FindingCount}}，执行失败巡检项：{{.Overview.FailedItemCount}}</li>
<li>Prometheus 查询：{{.Overview.QueryCount}} 次，总耗时 {{.Overview.QueryTotalMS}} ms，P95 {{.Overview.QueryP95MS}} ms</li>
</ul>
{{template "agg" dict "Title" "按业务（biz）" "Rows" .Overview.ByBiz}}
{{template "agg" dict "Title" "按机房（dc）" "Rows" .Overview.ByDC}}
{{template "agg" dict "Title" "按环境（env）" "Rows" .Overview.ByEnv}}

<h2 id="groups">二、问题组</h2>
{{if not .Groups}}<p>本次巡检未发现问题。</p>{{end}}
{{range $i, $g := .Groups}}
<h3>{{inc $i}}. {{$g.Title}}</h3>
<ul>
<li>巡检项：{{$g.CheckName}}　级别：<span class="{{$g.Level}}">{{$g.LevelText}}</span>　严重度：{{$g.Severity}}　影响主机：{{$g.Count}} 台</li>
{{if $g.Common}}<li>共同特征：{{join $g.Common "，"}}</li>{{end}}
{{if $g.Advice}}<li>处置参考：{{$g.Advice}}</li>{{end}}
<li>受影响主机：{{join $g.Hosts "，"}}</li>
</ul>
{{with $g.AI}}<div class="ai"><h4>AI 分析</h4><ul>
{{if .Phenomenon}}<li>现象：{{.Phenomenon}}</li>{{end}}
{{range .Causes}}<li>可能原因：{{.}}</li>{{end}}
{{range .Steps}}<li>排查步骤：{{.}}</li>{{end}}
{{if .Impact}}<li>影响：{{.Impact}}</li>{{end}}
{{range .Risks}}<li>风险：{{.}}</li>{{end}}
{{if .Note}}<li>校验：{{.Note}}</li>{{end}}
</ul></div>{{end}}
{{end}}

<h2 id="hosts">三、单机详情</h2>
{{if not .Hosts}}<p>没有存在发现项的主机。</p>{{end}}
{{range .Hosts}}
<h3>{{.Name}}（<span class="{{.Status}}">{{.StatusText}}</span>，健康分 {{.Score}}）</h3>
<ul>
{{if .Labels}}<li>标签：{{.Labels}}</li>{{end}}
{{if .DownText}}<li>状态：{{.DownText}}</li>{{end}}
{{range .Findings}}<li class="finding">[<span class="{{.Level}}">{{.LevelText}}</span>] {{.CheckName}}：当前值 <code>{{.Value}}</code>，阈值 <code>{{.Threshold}}</code>{{if .Extra}}，{{.Extra}}{{end}}，PromQL：<code>{{.PromQL}}</code>（{{.QueryID}}）</li>
{{end}}</ul>
{{end}}

<h2 id="top">四、Top N</h2>
{{range .Top}}
<h3>{{.Title}}</h3>
{{if .Entries}}<ol>{{range .Entries}}<li>{{.Host}}：{{.Value}}{{if .Extra}}（{{.Extra}}）{{end}}</li>{{end}}</ol>{{else}}<p>暂无数据。</p>{{end}}
{{end}}

<h2 id="missing-labels">五、缺标签主机</h2>
{{if .MissingLabels}}<p>以下主机缺少企业标签（env/role/biz/dc/owner），会影响分组与归属：</p>
<ul>{{range .MissingLabels}}<li>{{.Host}}：缺少 {{.Missing}}</li>{{end}}</ul>{{else}}<p>所有主机标签完整。</p>{{end}}

<h2 id="assets">六、资产变化</h2>
<p>{{.Assets.Summary}}</p>
{{if .Assets.Lines}}<ul>{{range .Assets.Lines}}<li>{{.}}</li>{{end}}</ul>{{end}}

<h2 id="unclassified">七、未分类的 Job</h2>
{{if .Unclassified}}<p>以下 job 未匹配任何服务规则（未分类），请在 config/services.yaml 中补充规则：</p>
<ul>{{range .Unclassified}}<li>{{.Job}}：{{.Targets}} 个目标，主机 {{dash .Hosts}}</li>{{end}}</ul>{{else}}<p>所有 job 均已被服务规则识别。</p>{{end}}

<h2 id="down">八、目标宕机与原因</h2>
{{if .Down}}<ul>{{range .Down}}<li>{{.Host}}：{{.Cause}}；失败目标 job：{{dash .Jobs}}；lastError：{{dash .LastError}}</li>{{end}}</ul>{{else}}<p>所有采集目标正常。</p>{{end}}

{{if .FailedItems}}
<h2 id="failed">九、执行失败的巡检项</h2>
<ul>{{range .FailedItems}}<li>{{.Name}}（{{.ID}}）：执行失败，错误：{{.Error}}，PromQL：<code>{{.PromQL}}</code></li>{{end}}</ul>
{{end}}

<h2 id="appendix">附录：巡检项与来源</h2>
<p>{{.SourceNote}}</p>
<ul>{{range .Items}}<li>{{.Name}}（{{.ID}}）：{{.Result}}，发现 {{.Findings}} 项，来源：{{.Source}}</li>{{end}}</ul>
{{if .Warnings}}<h3>告警信息</h3><ul>{{range .Warnings}}<li>{{.}}</li>{{end}}</ul>{{end}}
</body>
</html>
{{define "agg"}}<p><strong>{{.Title}}</strong></p>
{{if .Rows}}<table><tr><th>名称</th><th>主机数</th><th>绿</th><th>黄</th><th>红</th><th>平均健康分</th><th>状态</th></tr>
{{range .Rows}}<tr><td>{{.Name}}</td><td>{{.Hosts}}</td><td>{{.Green}}</td><td>{{.Yellow}}</td><td>{{.Red}}</td><td>{{.AvgScore}}</td><td class="{{.Status}}">{{.StatusText}}</td></tr>
{{end}}</table>{{else}}<p>暂无数据。</p>{{end}}{{end}}
`
