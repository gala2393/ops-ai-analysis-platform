package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yunwei/internal/config"
	"yunwei/internal/mock"
	"yunwei/internal/store"
)

// mockServer starts the in-process mock Prometheus for runner/CLI tests.
func mockServer(t *testing.T) *httptest.Server {
	t.Helper()
	sc, err := config.LoadScenario("../../config/mock-scenario.yaml")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := mock.New(sc, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// tempBundle writes app.yaml with its own report directory and database into
// a temp directory, reusing the real inspection and service rules.
func tempBundle(t *testing.T, promURL, outDir string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"inspection.yaml", "services.yaml"} {
		body, err := os.ReadFile(filepath.Join("../../config", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	app := "prometheus: { url: '" + promURL + "' }\n" +
		"storage: { path: '" + filepath.ToSlash(filepath.Join(dir, "y.db")) + "' }\n" +
		"report: { out_dir: '" + filepath.ToSlash(outDir) + "', top_n: 5 }\n" +
		"llm: { enabled: false }\n"
	if err := os.WriteFile(filepath.Join(dir, "app.yaml"), []byte(app), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "app.yaml")
}

func assertReportFiles(t *testing.T, dir string, runID int64) (md, html string) {
	t.Helper()
	md = filepath.Join(dir, fmt.Sprintf("%d.md", runID))
	html = filepath.Join(dir, fmt.Sprintf("%d.html", runID))
	for _, p := range []string{md, html} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Errorf("report file missing: %s: %v", p, err)
		} else if fi.Size() == 0 {
			t.Errorf("report file empty: %s", p)
		}
	}
	return md, html
}

// Every completed run, whatever the trigger, must write <runID>.md and
// <runID>.html into report.out_dir. The CLI keeps printing both paths.
func TestCompletedRunsWriteReportFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the 180 host mock")
	}
	ts := mockServer(t)
	outDir := t.TempDir()
	cfgPath := tempBundle(t, ts.URL, outDir)

	// A schedule-triggered run through the runner and its hook writes files.
	b, err := config.LoadBundle(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	runner, st, err := newRunner(b)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runner.OnSuccess = reportFileHook(b, "")
	out, err := runner.Run(context.Background(), store.TriggerSchedule)
	if err != nil {
		t.Fatal(err)
	}
	if out.ReportErr != nil {
		t.Fatalf("report write failed: %v", out.ReportErr)
	}
	md, html := assertReportFiles(t, outDir, out.Run.ID)
	if out.ReportFiles == nil || out.ReportFiles.Markdown != md || out.ReportFiles.HTML != html {
		t.Errorf("report files = %+v, want %s / %s", out.ReportFiles, md, html)
	}
	body, err := os.ReadFile(md)
	if err != nil || !strings.Contains(string(body), "运维巡检报告") {
		t.Errorf("markdown report missing title: err=%v", err)
	}

	// The CLI still writes reports and prints both paths.
	code, stdout, stderr := runCmd("inspect", "-config", cfgPath)
	if code != 0 {
		t.Fatalf("inspect code=%d stderr=%q", code, stderr)
	}
	var printed []string
	for _, line := range strings.Split(stdout, "\n") {
		if p, ok := strings.CutPrefix(line, "报告 (report): "); ok {
			printed = append(printed, strings.TrimSpace(p))
		}
	}
	if len(printed) != 2 {
		t.Fatalf("printed report paths = %v, stdout:\n%s", printed, stdout)
	}
	for _, p := range printed {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("printed report path does not exist: %s: %v", p, err)
		}
		if filepath.Dir(p) != outDir {
			t.Errorf("report %s not in configured out_dir %s", p, outDir)
		}
	}
	assertReportFiles(t, outDir, out.Run.ID+1)
}
