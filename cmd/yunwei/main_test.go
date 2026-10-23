package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCmd(args ...string) (code int, stdout, stderr string) {
	var o, e bytes.Buffer
	code = run(args, &o, &e)
	return code, o.String(), e.String()
}

func TestUsageAndUnknownCommand(t *testing.T) {
	if code, _, errOut := runCmd(); code != 2 || !strings.Contains(errOut, "serve") {
		t.Errorf("no args: code=%d stderr=%q", code, errOut)
	}
	if code, out, _ := runCmd("help"); code != 0 || !strings.Contains(out, "mock-prom") {
		t.Errorf("help: code=%d", code)
	}
	if code, _, errOut := runCmd("bogus"); code != 2 || !strings.Contains(errOut, "bogus") {
		t.Errorf("unknown: code=%d stderr=%q", code, errOut)
	}
	if code, _, _ := runCmd("serve", "-nope"); code != 2 {
		t.Errorf("bad flag: code=%d", code)
	}
}

func TestStubsPrintNotImplemented(t *testing.T) {
	for _, c := range [][]string{{"mock-prom"}, {"mock-notify"}} {
		code, _, errOut := runCmd(c...)
		if code != 1 || !strings.Contains(errOut, "not implemented") {
			t.Errorf("%v: code=%d stderr=%q", c, code, errOut)
		}
	}
}

const (
	okApp = "prometheus: { url: 'http://127.0.0.1:18090' }\nllm:\n  model: '${YW_CLI_MODEL}'\n  api_key: '${YW_CLI_KEY}'\n"
	okSvc = "services:\n  - id: node\n    name: n\n    match: { jobs: [node] }\n"
	okIns = "items:\n  - id: a\n    name: A\n    applies_to: [node]\n    query: up\n    compare: '>'\n    warn: 1\n"
)

func bundle(t *testing.T, app, ins, svc string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"app.yaml": app, "inspection.yaml": ins, "services.yaml": svc} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "app.yaml")
}

func TestInvalidConfigFailsCleanly(t *testing.T) {
	tests := []struct {
		name          string
		app, ins, svc string
		want          []string
	}{
		{"malformed inspection yaml", okApp, "items: [", okSvc, []string{"inspection.yaml", "YAML"}},
		{"missing query", okApp, strings.Replace(okIns, "    query: up\n", "", 1), okSvc, []string{"items[0].query"}},
		{"bad compare", okApp, strings.Replace(okIns, "'>'", "'=>'", 1), okSvc, []string{"items[0].compare"}},
		{"unknown service", okApp, strings.Replace(okIns, "[node]", "[ghost]", 1), okSvc, []string{"items[0].applies_to[0]", "ghost"}},
		{"bad cron", okApp + "schedule: ['x']\n", okIns, okSvc, []string{"app.yaml", "schedule[0]"}},
		{"missing prometheus url", "server: {listen: '127.0.0.1:1'}\n", okIns, okSvc, []string{"prometheus.url"}},
	}
	for _, sub := range []string{"inspect", "serve", "check-config"} {
		for _, tt := range tests {
			t.Run(sub+"/"+tt.name, func(t *testing.T) {
				code, _, errOut := runCmd(sub, "-config", bundle(t, tt.app, tt.ins, tt.svc))
				if code != 1 {
					t.Fatalf("code = %d, stderr=%q", code, errOut)
				}
				for _, w := range tt.want {
					if !strings.Contains(errOut, w) {
						t.Errorf("stderr %q does not mention %q", errOut, w)
					}
				}
				if strings.Contains(errOut, "goroutine ") || strings.Contains(errOut, "panic") {
					t.Errorf("stack trace leaked: %q", errOut)
				}
			})
		}
	}
	code, _, errOut := runCmd("inspect", "-config", filepath.Join(t.TempDir(), "missing", "app.yaml"))
	if code != 1 || !strings.Contains(errOut, "app.yaml") {
		t.Errorf("missing file: code=%d stderr=%q", code, errOut)
	}
}

func TestCheckConfigExpandsEnvAndRedacts(t *testing.T) {
	path := bundle(t, okApp, okIns, okSvc)
	t.Setenv("YW_CLI_MODEL", "model-from-env")
	t.Setenv("YW_CLI_KEY", "sk-do-not-print")

	code, out, errOut := runCmd("check-config", "-config", path)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "model-from-env") {
		t.Errorf("model not expanded in output:\n%s", out)
	}
	if strings.Contains(out, "sk-do-not-print") || strings.Contains(errOut, "sk-do-not-print") {
		t.Error("API key leaked")
	}
	if !strings.Contains(out, "***") {
		t.Errorf("expected redaction mark:\n%s", out)
	}
}

func TestCheckPromAgainstFakeServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/status/buildinfo":
			_, _ = w.Write([]byte(`{"status":"success","data":{"version":"3.1.0","goVersion":"go1"}}`))
		case "/api/v1/targets":
			_, _ = w.Write([]byte(`{"status":"success","data":{"activeTargets":[
{"labels":{"job":"node","instance":"a:9100"},"health":"up"},
{"labels":{"job":"node","instance":"b:9100"},"health":"down","lastError":"no route to host"}],"droppedTargets":[]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	app := "prometheus: { url: '" + srv.URL + "' }\n"
	code, out, errOut := runCmd("check-prom", "-config", bundle(t, app, okIns, okSvc))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	for _, want := range []string{"3.1.0", "共 2 个", "异常 1", "no route to host", "P95"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	srv.Close()
	if code, _, _ := runCmd("check-prom", "-config", bundle(t, app, okIns, okSvc)); code != 1 {
		t.Errorf("unreachable server should exit 1, got %d", code)
	}
}
