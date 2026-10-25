package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"yunwei/internal/config"
	"yunwei/internal/inspect"
	"yunwei/internal/prom"
	"yunwei/internal/store"
)

// newRunner opens the database and builds the inspection runner. The lock TTL
// can be shortened with YUNWEI_RUN_LOCK_TTL (a Go duration) for recovery tests.
func newRunner(b *config.Bundle) (*inspect.Runner, *store.Store, error) {
	pc := b.App.Prometheus
	client, err := prom.New(prom.Config{
		URL: pc.URL, Timeout: pc.Timeout, MaxConcurrency: pc.MaxConcurrency,
		MaxRange: pc.MaxRange, MinStep: pc.MinStep,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("无法创建 Prometheus 客户端: %w", err)
	}
	st, err := store.Open(b.App.Storage.Path)
	if err != nil {
		return nil, nil, fmt.Errorf("无法打开数据库 (cannot open database): %w", err)
	}
	r := &inspect.Runner{Store: st, Prom: client, Config: b}
	if v := os.Getenv("YUNWEI_RUN_LOCK_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second {
			st.Close()
			return nil, nil, fmt.Errorf("YUNWEI_RUN_LOCK_TTL %q 无效 (invalid duration)", v)
		}
		r.LockTTL = d
	}
	return r, st, nil
}

func runInspect(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("inspect", stderr)
	cfgPath := fs.String("config", defaultConfigPath, "app.yaml 路径 (path to app.yaml)")
	outDir := fs.String("out", "", "报告输出目录, 默认取配置 report.out_dir (report output dir)")
	if code, done := parseFlags(fs, args); done {
		return code
	}
	b, err := loadConfig(*cfgPath, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "配置错误 (config error):\n%v\n", err)
		return 1
	}
	runner, st, err := newRunner(b)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer st.Close()
	runner.OnSuccess = reportFileHook(b, *outDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	out, err := runner.Run(ctx, store.TriggerCLI)
	if errors.Is(err, inspect.ErrRunning) {
		fmt.Fprintln(stderr, "已有巡检在运行,本次未执行 (an inspection is already running)")
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "巡检失败 (inspection failed): %v\n", err)
		return 1
	}
	printSummary(stdout, out)
	if out.ReportErr != nil {
		fmt.Fprintf(stderr, "写入报告失败 (report write failed): %v\n", out.ReportErr)
		return 1
	}
	fmt.Fprintf(stdout, "报告 (report): %s\n", out.ReportFiles.Markdown)
	fmt.Fprintf(stdout, "报告 (report): %s\n", out.ReportFiles.HTML)
	return 0
}

func printSummary(w io.Writer, out *inspect.Outcome) {
	r := out.Run
	fmt.Fprintf(w, "巡检完成 (inspection finished): run #%d  触发=%s  状态=%s\n", r.ID, r.Trigger, r.Status)
	fmt.Fprintf(w, "主机 (hosts): 共 %d, 正常 %d, 关注 %d, 异常 %d\n", r.Hosts.Total, r.Hosts.OK, r.Hosts.Warn, r.Hosts.Crit)
	fmt.Fprintf(w, "问题分组 (groups): %d  发现项 (findings): %d\n", r.Groups, r.Findings)
	fmt.Fprintf(w, "查询统计 (queries): %d 次, 总耗时 %d ms, P95 %d ms\n", r.Query.Count, r.Query.TotalMS, r.Query.P95MS)
	fmt.Fprintf(w, "耗时 (duration): %.1f s\n", float64(r.DurationMS)/1000)
	d := out.Diff
	if !d.HasPrevious {
		fmt.Fprintln(w, "资产变化 (asset changes): 无上次巡检记录 (no previous run)")
	} else {
		fmt.Fprintf(w, "资产变化 (asset changes, vs run #%d): 新增主机 %d, 移除主机 %d, 服务新增 %d 台, 服务移除 %d 台\n",
			d.PreviousRunID, len(d.AddedHosts), len(d.RemovedHosts), len(d.ServicesAdded), len(d.ServicesRemoved))
		for _, h := range d.AddedHosts {
			fmt.Fprintf(w, "  + 新增主机 %s\n", h.Name)
		}
		for _, h := range d.RemovedHosts {
			fmt.Fprintf(w, "  - 移除主机 %s\n", h.Name)
		}
		for _, c := range d.ServicesAdded {
			fmt.Fprintf(w, "  + 服务新增 %s: %v\n", c.Name, c.Services)
		}
		for _, c := range d.ServicesRemoved {
			fmt.Fprintf(w, "  - 服务移除 %s: %v\n", c.Name, c.Services)
		}
	}
	for _, it := range out.Result.Errors() {
		fmt.Fprintf(w, "警告: 巡检项 %s 执行失败: %s\n", it.ID, it.Error)
	}
}
