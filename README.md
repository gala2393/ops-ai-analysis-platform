# 运维 AI 分析平台

一个基于 Go 和 Prometheus 的运维巡检与分析平台。平台从监控系统只读获取指标，使用规则完成异常判定，再生成带有 PromQL、实际数值和主机范围的可追溯报告，并通过 Web 界面提供总览、主机筛选、报告查看、手动巡检和反馈入口。

> 当前状态：MVP 与 Web v1 已可运行。AI 结构化分析、Alertmanager 生产接入、真实通知渠道和权限体系仍在迭代中。

## 已实现能力

- Prometheus HTTP API 只读访问，支持查询超时、并发和时间范围约束。
- 从 exporter 目标自动发现主机，并按主机归并多个 exporter 实例。
- 根据 job、app、指标前缀和健康状态识别 Prometheus、Node Exporter、MySQL 等服务。
- YAML 配置巡检项、适用服务、阈值、严重度、PromQL 和处置建议。
- CPU、内存、Swap、磁盘、inode、目标可用性、重启等通用巡检能力。
- 异常问题分组、健康评分、业务/环境/机房汇总和 Top N 排行。
- SQLite 保存巡检运行记录、发现项和用户反馈。
- Markdown 和 HTML 报告，包含 PromQL、查询编号、实际数值、阈值和受影响主机。
- Web 总览、主机列表、主机详情、报告列表、报告详情和趋势图。
- 手动触发巡检、运行进度轮询和报告反馈。
- Mock Prometheus，可生成 180 台主机和预置异常，用于本地演示与验收。
- 完整 Go 单元测试覆盖配置、Prometheus 客户端、发现、巡检、报告、存储和 Web 层。

## 技术栈

- Go 1.22+
- `net/http`、`chi`
- Prometheus HTTP API 与 `client_golang`
- SQLite（`modernc.org/sqlite`，无需 CGO）
- YAML 配置（`gopkg.in/yaml.v3`）
- `html/template`、htmx
- `robfig/cron` 定时巡检

## 快速运行

### 编译与配置检查

Windows PowerShell 或 CMD：

```bat
cd /d E:\yunwei
go build ./cmd/yunwei
yunwei.exe check-config -config config\app.yaml
```

编译后生成 `yunwei.exe`。注意命令是 `build`，不是 `bulid`。

### 启动模拟环境

打开第一个终端并保持运行：

```bat
cd /d E:\yunwei
yunwei.exe mock-prom -scenario config\mock-scenario.yaml -listen 127.0.0.1:18090
```

打开第二个终端：

```bat
cd /d E:\yunwei
yunwei.exe serve -config config\app.yaml
```

浏览器访问：<http://127.0.0.1:18080/>

执行一次命令行巡检：

```bat
yunwei.exe inspect -config config\app.yaml -out reports
```

报告写入 `reports/`，同时生成 Markdown 和 HTML 文件。停止服务时在对应终端按 `Ctrl+C`。

## 真实 Prometheus

真实环境配置模板是 `config/app.real.yaml`。使用前请确认 Prometheus 地址、标签规范、exporter 状态和企业巡检标准：

```bat
yunwei.exe check-config -config config\app.real.yaml
yunwei.exe check-prom -config config\app.real.yaml
yunwei.exe serve -config config\app.real.yaml
```

项目不会主动连接服务器执行命令，只读取 Prometheus 数据。默认 `app.yaml` 指向本地 Mock Prometheus，因此页面中的 `app-sh1-023`、`web-gz1-005` 等是模拟主机，不是真实资产。

## 配置文件

- `config/app.yaml`：本地开发、Mock Prometheus 和 Web 服务配置。
- `config/app.real.yaml`：真实 Prometheus 环境模板。
- `config/inspection.yaml`：巡检项、PromQL、阈值、严重度和建议。
- `config/services.yaml`：服务识别规则。
- `config/mock-scenario.yaml`：模拟主机规模、标签和异常注入场景。
- `.env`：本地密钥和环境变量，不提交到 Git。

## 目录结构

```text
cmd/yunwei/         CLI 命令入口
internal/config/    配置加载与校验
internal/prom/      Prometheus 客户端和查询统计
internal/discovery/ 资产发现与服务识别
internal/inspect/   巡检引擎、阈值判定和调度
internal/report/    Markdown/HTML 报告
internal/store/     SQLite 持久化
internal/web/       Web 页面、API、趋势和反馈
internal/mock/      Mock Prometheus 数据服务
internal/notify/    通知 Mock 接收端
config/             配置与模拟场景
docs/               项目文档
reports/            本地生成报告，不纳入 Git
data/               本地 SQLite 数据，不纳入 Git
```

## 验证结果

当前开发环境已验证：

```text
go build ./cmd/yunwei       编译通过
go test ./...               全部测试通过
check-config                配置校验通过
mock-prom                   180 台主机、235 个 targets 启动成功
serve                       Web 服务在 127.0.0.1:18080 启动成功
GET /                       返回 HTTP 200
```

## 当前边界

已完成的是规则巡检、证据报告和 Web v1。以下能力还不应描述为已完成：

- LLM 结构化分析和证据引用校验的完整生产链路。
- Alertmanager webhook 到分析报告的生产闭环。
- 飞书、钉钉、企业微信真实推送。
- RAG、Runbook、Loki/ELK、变更系统和拓扑关联。
- 用户认证、权限控制、审计和在线配置编辑。

详细设计、里程碑、风险和验收标准见 [项目计划书.md](项目计划书.md)。
