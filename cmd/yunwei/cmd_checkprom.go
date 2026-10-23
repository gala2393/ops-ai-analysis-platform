package main

import (
	"context"
	"fmt"
	"io"

	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"

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
		if t.Health == promv1.HealthGood {
			continue
		}
		fmt.Fprintf(stdout, "  DOWN job=%s instance=%s error=%s\n", t.Labels["job"], t.Labels["instance"], t.LastError)
	}
	st := client.Stats()
	fmt.Fprintf(stdout, "查询统计: %d 次, 总耗时 %d ms, P95 %d ms\n", st.Count, st.TotalMS(), st.P95MS())
	return 0
}
