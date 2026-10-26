package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"yunwei/internal/inspect"
	"yunwei/internal/web"
)

// runServe starts the cron scheduler and the web server (HTML UI + JSON API).
func runServe(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("serve", stderr)
	cfgPath := fs.String("config", defaultConfigPath, "app.yaml 路径 (path to app.yaml)")
	if code, done := parseFlags(fs, args); done {
		return code
	}
	b, err := loadConfig(*cfgPath, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "配置错误 (config error):\n%v\n", err)
		return 1
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))
	runner, st, err := newRunner(b)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer st.Close()
	runner.Log = log
	// Scheduled and API-triggered runs write report files just like the CLI.
	runner.OnSuccess = reportFileHook(b, "")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	sched, err := inspect.NewScheduler(ctx, runner, b.App.Schedule, log)
	if err != nil {
		fmt.Fprintf(stderr, "定时任务配置错误: %v\n", err)
		return 1
	}
	sched.Start()
	defer sched.Stop()
	log.Info("scheduler started", "specs", b.App.Schedule)

	ui, err := web.New(st, b, runner.Prom, runner)
	if err != nil {
		fmt.Fprintf(stderr, "无法初始化网页界面: %v\n", err)
		return 1
	}
	srv := &http.Server{Addr: b.App.Server.Listen, Handler: ui.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	fmt.Fprintf(stdout, "yunwei serve 已启动: http://%s\n", b.App.Server.Listen)
	select {
	case err := <-errc:
		fmt.Fprintf(stderr, "HTTP 服务错误: %v\n", err)
		return 1
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	return 0
}
