package config

import (
	"strings"
	"testing"
)

const synthSecret = "sk-SYNTHETIC-YAML-SECRET-4e8b"

func TestYAMLErrorsNeverEchoSource(t *testing.T) {
	const base = "prometheus: {url: 'http://p'}\n"
	tests := []struct {
		name, yaml string
		secrets    []string
		want       []string
	}{
		{"unknown alias in api_key",
			base + "llm:\n  api_key: *" + synthSecret + "\n",
			[]string{synthSecret, "SYNTHETIC"}, []string{"app.yaml", "未定义锚点", "第 3 行"}},
		{"type error on secret field",
			base + "llm:\n  api_key: [" + synthSecret + "]\n",
			[]string{synthSecret, "SYNTHETIC"}, []string{"app.yaml", "llm.api_key", "类型错误", "第 3 行"}},
		{"type error on secret scalar in bool field",
			base + "notify:\n  wecom:\n    enabled: '" + synthSecret + "'\n",
			[]string{synthSecret, "SYNTHETIC"}, []string{"notify.wecom.enabled", "类型错误", "第 4 行"}},
		{"type error in flow style reports original line",
			"prometheus: {url: 'http://p'}\nnotify: {wecom: {enabled: '" + synthSecret + "'}}\n",
			[]string{synthSecret}, []string{"notify.wecom.enabled", "第 2 行"}},
		{"malformed quoting around secret",
			base + "llm:\n  api_key: \"" + synthSecret + "\\q\"\n",
			[]string{synthSecret, "SYNTHETIC"}, []string{"app.yaml", "语法错误", "第 3 行"}},
		{"unterminated quote around secret",
			base + "llm:\n  api_key: '" + synthSecret + "\n  model: x\n",
			[]string{synthSecret, "SYNTHETIC"}, []string{"语法错误"}},
		{"unknown field named like a secret",
			base + "llm:\n  " + synthSecret + ": 1\n",
			[]string{synthSecret, "SYNTHETIC"}, []string{"llm", "未知字段", "第 3 行"}},
		{"duplicate key named like a secret",
			base + "llm:\n  model: a\n  model: " + synthSecret + "\n  " + synthSecret + ": 1\n  " + synthSecret + ": 2\n",
			[]string{synthSecret, "SYNTHETIC"}, []string{"app.yaml"}},
		{"secret from env in wrong type",
			base + "notify: {wecom: {enabled: '${YW_SYNTH_SECRET}'}}\n",
			[]string{synthSecret, "SYNTHETIC"}, []string{"notify.wecom.enabled", "类型错误"}},
	}
	t.Setenv("YW_SYNTH_SECRET", synthSecret)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadApp(writeFile(t, t.TempDir(), "app.yaml", tt.yaml))
			if err == nil {
				t.Fatal("expected error")
			}
			for _, s := range tt.secrets {
				if strings.Contains(err.Error(), s) {
					t.Errorf("error leaks %q: %v", s, err)
				}
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not mention %q", err, w)
				}
			}
		})
	}
}

func TestYAMLErrorsSanitizedForEveryLoader(t *testing.T) {
	bad := map[string]string{
		"alias":  "x: *" + synthSecret + "\n",
		"syntax": "items: ['" + synthSecret + "\n",
		"type":   "items: " + synthSecret + "\n",
		"field":  synthSecret + ": 1\n",
	}
	loaders := map[string]func(string) error{
		"app":        func(p string) error { _, err := LoadApp(p); return err },
		"inspection": func(p string) error { _, err := LoadInspection(p); return err },
		"services":   func(p string) error { _, err := LoadServices(p); return err },
		"scenario":   func(p string) error { _, err := LoadScenario(p); return err },
	}
	for ln, load := range loaders {
		for bn, body := range bad {
			t.Run(ln+"/"+bn, func(t *testing.T) {
				err := load(writeFile(t, t.TempDir(), ln+".yaml", body))
				if err == nil {
					t.Fatal("expected error")
				}
				if strings.Contains(err.Error(), synthSecret) || strings.Contains(err.Error(), "SYNTHETIC") {
					t.Errorf("error echoes source: %v", err)
				}
				if !strings.Contains(err.Error(), ln+".yaml") {
					t.Errorf("error does not name the file: %v", err)
				}
			})
		}
	}
}
