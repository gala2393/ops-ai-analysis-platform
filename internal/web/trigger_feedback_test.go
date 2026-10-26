package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"yunwei/internal/inspect"
	"yunwei/internal/store"
)

// stubStarter implements RunStarter without touching Prometheus.
type stubStarter struct {
	id    int64
	err   error
	calls int
}

func (s *stubStarter) RunAsync(trigger string) (int64, error) {
	s.calls++
	return s.id, s.err
}

func newStarterServer(t *testing.T, starter RunStarter) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := New(st, testBundle(), nil, starter)
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	return srv, st
}

func post(t *testing.T, h http.Handler, path, contentType, body string, hx bool) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	h.ServeHTTP(rec, req)
	out, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, string(out)
}

func TestTriggerRunEndpoint(t *testing.T) {
	// Happy path: the run starts in the background, the API answers at once.
	starter := &stubStarter{id: 7}
	srv, _ := newStarterServer(t, starter)
	h := srv.Handler()

	code, body := post(t, h, "/api/runs", "", "", false)
	if code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", code, body)
	}
	if !strings.Contains(body, `"id":7`) || !strings.Contains(body, "巡检已启动") {
		t.Errorf("body = %s", body)
	}
	if starter.calls != 1 {
		t.Errorf("starter calls = %d, want 1", starter.calls)
	}

	// A concurrent run is rejected with the exact UI message and 409.
	busy := &stubStarter{err: inspect.ErrRunning}
	srv2, _ := newStarterServer(t, busy)
	code, body = post(t, srv2.Handler(), "/api/runs", "", "", false)
	if code != http.StatusConflict || !strings.Contains(body, "已有巡检在运行") {
		t.Errorf("concurrent: status = %d body = %s", code, body)
	}

	// Without a starter the endpoint degrades cleanly.
	srv3, _ := newStarterServer(t, nil)
	code, _ = post(t, srv3.Handler(), "/api/runs", "", "", false)
	if code != http.StatusServiceUnavailable {
		t.Errorf("no starter: status = %d, want 503", code)
	}
}

func TestTriggerButtonOnOverviewAndReports(t *testing.T) {
	srv, st := newStarterServer(t, &stubStarter{id: 1})
	seedRun(t, st)
	h := srv.Handler()
	for _, page := range []string{"/", "/reports"} {
		code, body, _ := get(t, h, page)
		if code != 200 {
			t.Fatalf("GET %s status = %d", page, code)
		}
		for _, want := range []string{`hx-post="/api/runs"`, "手动巡检", `id="run-progress"`} {
			if !strings.Contains(body, want) {
				t.Errorf("GET %s missing trigger element %q", page, want)
			}
		}
	}
}

func TestRunProgressFragment(t *testing.T) {
	srv, st := newStarterServer(t, nil)
	h := srv.Handler()
	ctx := context.Background()

	// A running run: the fragment shows progress and keeps polling via htmx.
	run, err := st.StartRun(ctx, store.TriggerManual, "test", time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetProgress(ctx, run.ID, 3, 14); err != nil {
		t.Fatal(err)
	}
	code, body, _ := get(t, h, "/runs/"+itoa(run.ID)+"/progress")
	if code != 200 {
		t.Fatalf("running progress status = %d", code)
	}
	for _, want := range []string{"hx-get=\"/runs/" + itoa(run.ID) + "/progress\"", "every 500ms", "巡检项 3 / 14"} {
		if !strings.Contains(body, want) {
			t.Errorf("running fragment missing %q", want)
		}
	}
	if strings.Contains(body, "/reports/") {
		t.Error("running fragment must not link to the report yet")
	}

	// A finished run: polling stops and the fragment links to the report.
	if err := st.CompleteRun(ctx, run.ID, store.Result{
		Hosts:      store.HostCounts{Total: 4, OK: 3, Crit: 1},
		FinishedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	// The runner releases the lock after completion; do the same here.
	if err := st.ReleaseLock(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	code, body, _ = get(t, h, "/runs/"+itoa(run.ID)+"/progress")
	if code != 200 {
		t.Fatalf("finished progress status = %d", code)
	}
	for _, want := range []string{"/reports/" + itoa(run.ID), "查看巡检报告", "已完成"} {
		if !strings.Contains(body, want) {
			t.Errorf("finished fragment missing %q", want)
		}
	}
	if strings.Contains(body, "every 500ms") {
		t.Error("finished fragment must stop polling")
	}

	// A failed run renders the error banner.
	run2, err := st.StartRun(ctx, store.TriggerManual, "test", time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FailRun(ctx, run2.ID, "boom", time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = st.ReleaseLock(ctx, run2.ID)
	code, body, _ = get(t, h, "/runs/"+itoa(run2.ID)+"/progress")
	if code != 200 || !strings.Contains(body, "boom") {
		t.Errorf("failed fragment: status = %d body = %s", code, body)
	}

	// Unknown run: 404 fragment with a Chinese notice, never a bare error.
	code, body, _ = get(t, h, "/runs/9999/progress")
	if code != 404 || !strings.Contains(body, "巡检不存在") {
		t.Errorf("missing run fragment: status = %d body = %s", code, body)
	}
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

func TestFeedbackAPIValidation(t *testing.T) {
	srv, st := newStarterServer(t, nil)
	runID := seedRun(t, st)
	h := srv.Handler()
	groupTarget := itoa(runID) + ":0"

	cases := []struct {
		name string
		body string
		want int
		msg  string
	}{
		{"bad rating", `{"target_type":"run_group","target_id":"` + groupTarget + `","rating":"great"}`, 400, "无效的评分"},
		{"empty rating", `{"target_type":"run_group","target_id":"` + groupTarget + `"}`, 400, "无效的评分"},
		{"bad target type", `{"target_type":"host","target_id":"` + groupTarget + `","rating":"useful"}`, 400, "无效的目标类型"},
		{"non-existent group", `{"target_type":"run_group","target_id":"999:0","rating":"useful"}`, 400, "反馈目标不存在"},
		{"group index out of range", `{"target_type":"run_group","target_id":"` + itoa(runID) + `:9","rating":"useful"}`, 400, "反馈目标不存在"},
		{"malformed target id", `{"target_type":"run_group","target_id":"abc","rating":"useful"}`, 400, "反馈目标不存在"},
		{"non-existent alert event", `{"target_type":"alert_event","target_id":"42","rating":"useful"}`, 400, "反馈目标不存在"},
		{"note too long", `{"target_type":"run_group","target_id":"` + groupTarget + `","rating":"useful","note":"` + strings.Repeat("长", 501) + `"}`, 400, "备注过长"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := post(t, h, "/api/feedback", "application/json", tc.body, false)
			if code != tc.want {
				t.Errorf("status = %d, want %d; body = %s", code, tc.want, body)
			}
			if !strings.Contains(body, tc.msg) {
				t.Errorf("body = %s, want %q", body, tc.msg)
			}
		})
	}

	// Malformed JSON is a 400, not a 500.
	code, _ := post(t, h, "/api/feedback", "application/json", "{not json", false)
	if code != 400 {
		t.Errorf("malformed JSON status = %d, want 400", code)
	}

	// A valid request is stored and acknowledged.
	code, body := post(t, h, "/api/feedback", "application/json",
		`{"target_type":"run_group","target_id":"`+groupTarget+`","rating":"useful","note":"很有参考价值"}`, false)
	if code != http.StatusCreated {
		t.Fatalf("valid feedback status = %d body = %s", code, body)
	}
	var fb store.Feedback
	if err := json.Unmarshal([]byte(body), &fb); err != nil {
		t.Fatalf("response not a feedback object: %v (%s)", err, body)
	}
	if fb.ID == 0 || fb.Rating != "useful" || fb.Note != "很有参考价值" || fb.TargetType != "run_group" {
		t.Errorf("stored feedback = %+v", fb)
	}
}

func TestFeedbackDisplayedOnReportAndHTMXFragment(t *testing.T) {
	srv, st := newStarterServer(t, nil)
	runID := seedRun(t, st)
	h := srv.Handler()
	groupTarget := itoa(runID) + ":0"

	// Before any feedback the widget renders the empty hint.
	_, body, _ := get(t, h, "/reports/"+itoa(runID))
	if !strings.Contains(body, "尚无反馈") {
		t.Error("report page should show the empty feedback hint")
	}

	// htmx form posts get the refreshed widget fragment back (200, HTML).
	form := url.Values{
		"target_type": {"run_group"},
		"target_id":   {groupTarget},
		"rating":      {"useful"},
		"note":        {"阈值合理"},
	}
	code, frag := post(t, h, "/api/feedback", "application/x-www-form-urlencoded", form.Encode(), true)
	if code != 200 {
		t.Fatalf("htmx feedback status = %d body = %s", code, frag)
	}
	for _, want := range []string{"有用", "阈值合理", "已收到 1 条反馈"} {
		if !strings.Contains(frag, want) {
			t.Errorf("fragment missing %q", want)
		}
	}
	if strings.Contains(frag, "<!DOCTYPE html>") {
		t.Error("feedback htmx response must be a fragment")
	}

	// After a full page reload the stored feedback is rendered on the report.
	_, body, _ = get(t, h, "/reports/"+itoa(runID))
	for _, want := range []string{"有用", "阈值合理"} {
		if !strings.Contains(body, want) {
			t.Errorf("reloaded report missing feedback %q", want)
		}
	}

}

// TestFeedbackSurvivesServerRestart mirrors VAL-WEB-014: feedback submitted
// through the API is still rendered after the store is closed and the server
// rebuilt on the same database (what a serve restart does).
func TestFeedbackSurvivesServerRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "restart.db")

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(st, testBundle(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	runID := seedRun(t, st)
	code, body := post(t, srv.Handler(), "/api/feedback", "application/json",
		`{"target_type":"run_group","target_id":"`+itoa(runID)+`:0","rating":"wrong","note":"数值与截图不符"}`, false)
	if code != http.StatusCreated {
		t.Fatalf("submit: status = %d body = %s", code, body)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st2, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st2.Close() })
	srv2, err := New(st2, testBundle(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	code, page, _ := get(t, srv2.Handler(), "/reports/"+itoa(runID))
	if code != 200 {
		t.Fatalf("report after restart status = %d", code)
	}
	for _, want := range []string{"错误", "数值与截图不符"} {
		if !strings.Contains(page, want) {
			t.Errorf("report after restart missing feedback %q", want)
		}
	}
}
func TestReportDownload(t *testing.T) {
	dir := t.TempDir()
	md := "# 巡检报告 #1\n\n测试内容"
	html := "<html><body>报告</body></html>"
	if err := os.WriteFile(filepath.Join(dir, "1.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1.html"), []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	b := testBundle()
	b.App.Report.OutDir = dir
	srv, err := New(st, b, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seedRun(t, st)
	h := srv.Handler()

	// The detail page links to both downloads.
	_, body, _ := get(t, h, "/reports/1")
	for _, want := range []string{"/reports/1/download/md", "/reports/1/download/html"} {
		if !strings.Contains(body, want) {
			t.Errorf("report detail missing download link %q", want)
		}
	}

	code, got, hdr := get(t, h, "/reports/1/download/md")
	if code != 200 || got != md {
		t.Errorf("md download: status = %d body = %q", code, got)
	}
	if !strings.Contains(hdr.Get("Content-Type"), "text/markdown") || !strings.Contains(hdr.Get("Content-Disposition"), "attachment") {
		t.Errorf("md headers: %v", hdr)
	}
	code, got, hdr = get(t, h, "/reports/1/download/html")
	if code != 200 || got != html {
		t.Errorf("html download: status = %d body = %q", code, got)
	}
	if !strings.Contains(hdr.Get("Content-Type"), "text/html") {
		t.Errorf("html content-type = %q", hdr.Get("Content-Type"))
	}

	// Unknown kind and missing files are 404.
	if code, _, _ := get(t, h, "/reports/1/download/pdf"); code != 404 {
		t.Errorf("bad kind status = %d, want 404", code)
	}
	if code, _, _ := get(t, h, "/reports/2/download/md"); code != 404 {
		t.Errorf("missing file status = %d, want 404", code)
	}
}
