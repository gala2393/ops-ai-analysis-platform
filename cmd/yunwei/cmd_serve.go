package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"time"

	"yunwei/internal/inspect"
	"yunwei/internal/store"
)

// runServe starts the cron scheduler and the HTTP API. The web UI handlers
// replace the minimal run endpoints below.
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

	srv := &http.Server{Addr: b.App.Server.Listen, Handler: runsHandler(st), ReadHeaderTimeout: 10 * time.Second}
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

func runsHandler(st *store.Store) http.Handler {
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/runs", func(w http.ResponseWriter, r *http.Request) {
		runs, err := st.ListRuns(r.Context(), 200)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if runs == nil {
			runs = []*store.Run{}
		}
		writeJSON(w, http.StatusOK, runs)
	})
	mux.HandleFunc("GET /api/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid run id"})
			return
		}
		d, err := st.LoadRun(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "run not found"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"run": d.Run, "detail": d.Detail, "findings": d.Findings, "groups": d.Groups, "hosts": d.Hosts,
		})
	})
	return mux
}
