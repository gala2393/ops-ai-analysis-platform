package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"

	"yunwei/internal/config"
	"yunwei/internal/discovery"
	"yunwei/internal/prom"
)

// runCheckProm prints connectivity, version and target health. It only uses
// read-only query endpoints.
func runCheckProm(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("check-prom", stderr)
	cfgPath := fs.String("config", defaultConfigPath, "app.yaml 路径 (path to app.yaml)")
	if code, done := parseFlags(fs, args); done {
		return code
	}
	b, err := loadConfig(*cfgPath, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "配置错误 (config error):\n%v\n", err)
		return 1
	}
	pc := b.App.Prometheus
	client, err := prom.New(prom.Config{
		URL: pc.URL, Timeout: pc.Timeout, MaxConcurrency: pc.MaxConcurrency,
		MaxRange: pc.MaxRange, MinStep: pc.MinStep,
	})
	if err != nil {
		fmt.Fprintf(stderr, "无法创建 Prometheus 客户端: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*pc.Timeout)
	defer cancel()

	fmt.Fprintf(stdout, "Prometheus: %s\n", pc.URL)
	bi, err := client.Buildinfo(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "连接失败 (cannot reach Prometheus): %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "版本 (version): %s (go %s)\n", bi.Version, bi.GoVersion)

	tg, err := client.Targets(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "读取 targets 失败: %v\n", err)
		return 1
	}
	up := 0
	for _, t := range tg.Active {
		if t.Health == promv1.HealthGood {
			up++
		}
	}
	fmt.Fprintf(stdout, "Targets: 共 %d 个, 正常 %d, 异常 %d\n", len(tg.Active), up, len(tg.Active)-up)
	for _, t := range tg.Active {
		fmt.Fprintf(stdout, "  %-4s job=%s instance=%s", strings.ToUpper(string(t.Health)), t.Labels["job"], t.Labels["instance"])
		if t.LastError != "" {
			fmt.Fprintf(stdout, " lastError=%s", t.LastError)
		}
		fmt.Fprintln(stdout)
	}
	printHosts(ctx, stdout, stderr, client, b)
	st := client.Stats()
	fmt.Fprintf(stdout, "查询统计: %d 次, 总耗时 %d ms, P95 %d ms\n", st.Count, st.TotalMS(), st.P95MS())
	return 0
}

// printHosts merges targets into hosts and prints each host's status and, for
// hosts with down targets, the classified cause. A discovery failure only
// produces a warning because connectivity was already proven.
func printHosts(ctx context.Context, stdout, stderr io.Writer, client *prom.Client, b *config.Bundle) {
	res, err := discovery.Discover(ctx, client.Scoped(), b.Services, b.App.Labels, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "主机发现失败 (discovery failed): %v\n", err)
		return
	}
	down := 0
	for _, h := range res.Hosts {
		if !h.Normal() {
			down++
		}
	}
	fmt.Fprintf(stdout, "主机 (hosts): 共 %d 台, 正常 %d, 异常 %d\n", len(res.Hosts), len(res.Hosts)-down, down)
	for _, h := range res.Hosts {
		fmt.Fprintf(stdout, "  %-16s status=%s services=%s", h.Name, h.Status, strings.Join(h.ServiceIDs(), ","))
		if !h.Normal() {
			fmt.Fprintf(stdout, " cause=%s (%s) down_jobs=%s error=%s", h.DownCause, h.DownText, strings.Join(h.DownJobs, ","), h.DownError)
		}
		fmt.Fprintln(stdout)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(stderr, "警告 (warning): %s\n", w)
	}
}
