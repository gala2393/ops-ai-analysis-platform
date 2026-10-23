package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"time"

	"yunwei/internal/config"
	"yunwei/internal/mock"
)

// runMockProm serves the mock Prometheus until interrupted.
func runMockProm(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("mock-prom", stderr)
	scenarioPath := fs.String("scenario", "config/mock-scenario.yaml", "场景文件 (scenario file)")
	listen := fs.String("listen", "127.0.0.1:18090", "监听地址 (listen address)")
	expectedOut := fs.String("expected-out", "", "期望异常清单输出路径 (expected-anomaly manifest path)")
	if code, done := parseFlags(fs, args); done {
		return code
	}
	sc, err := config.LoadScenario(*scenarioPath)
	if err != nil {
		fmt.Fprintf(stderr, "场景配置错误 (scenario error):\n%v\n", err)
		return 1
	}
	// The TSDB head dominates memory; keep the GC from letting garbage pile up.
	debug.SetMemoryLimit(400 << 20)

	log := slog.New(slog.NewTextHandler(stderr, nil))
	began := time.Now()
	srv, err := mock.New(sc, log)
	if err != nil {
		fmt.Fprintf(stderr, "无法初始化 mock (cannot start mock): %v\n", err)
		return 1
	}
	defer srv.Close()
	if *expectedOut != "" {
		if err := srv.Fleet().WriteManifest(*expectedOut); err != nil {
			fmt.Fprintf(stderr, "写入期望清单失败 (cannot write manifest): %v\n", err)
			return 1
		}
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(stderr, "无法监听 (cannot listen): %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "mock-prom listening on %s: hosts=%d targets=%d history=%s step=%s live=%s (ready in %s)\n",
		ln.Addr(), len(srv.Fleet().Hosts), srv.Fleet().TargetCount(), sc.History, sc.Step, sc.LiveInterval,
		time.Since(began).Round(time.Millisecond))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := srv.Serve(ctx, ln); err != nil {
		fmt.Fprintf(stderr, "mock-prom 异常退出: %v\n", err)
		return 1
	}
	return 0
}
