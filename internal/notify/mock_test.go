package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func do(t *testing.T, h http.Handler, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, string(out)
}

type listing struct {
	Fail  bool       `json:"fail"`
	Count int        `json:"count"`
	Items []Received `json:"items"`
}

func list(t *testing.T, h http.Handler) listing {
	t.Helper()
	code, out := do(t, h, http.MethodGet, "/received", "")
	if code != http.StatusOK {
		t.Fatalf("GET /received = %d", code)
	}
	var l listing
	if err := json.Unmarshal([]byte(out), &l); err != nil {
		t.Fatalf("bad listing %q: %v", out, err)
	}
	return l
}

func TestPlatformRepliesAndRecording(t *testing.T) {
	h := NewMockReceiver().Handler()
	tests := []struct {
		path, body, reply string
	}{
		{"/feishu", `{"msg_type":"text","content":{"text":"hi"}}`, `{"code":0,"msg":"success"}`},
		{"/dingtalk", `{"msgtype":"markdown","markdown":{"title":"t","text":"x"}}`, `{"errcode":0,"errmsg":"ok"}`},
		{"/wecom", `{"msgtype":"markdown","markdown":{"content":"x"}}`, `{"errcode":0,"errmsg":"ok"}`},
	}
	for _, tt := range tests {
		code, out := do(t, h, http.MethodPost, tt.path, tt.body)
		if code != 200 || strings.TrimSpace(out) != tt.reply {
			t.Errorf("%s: code=%d body=%q", tt.path, code, out)
		}
	}
	l := list(t, h)
	if l.Count != 3 || len(l.Items) != 3 {
		t.Fatalf("count=%d items=%d", l.Count, len(l.Items))
	}
	for i, tt := range tests {
		it := l.Items[i]
		if it.Path != tt.path || it.Channel != tt.path[1:] || it.Time.IsZero() || string(it.Body) != tt.body || it.Seq != i+1 {
			t.Errorf("item %d = %+v", i, it)
		}
	}
}

func TestNonJSONBodyStaysWellFormed(t *testing.T) {
	h := NewMockReceiver().Handler()
	do(t, h, http.MethodPost, "/wecom", "not json")
	l := list(t, h)
	if len(l.Items) != 1 || string(l.Items[0].Body) != `"not json"` {
		t.Errorf("items = %+v", l.Items)
	}
}

func TestBOMPrefixedJSONIsParsed(t *testing.T) {
	h := NewMockReceiver().Handler()
	do(t, h, http.MethodPost, "/feishu", "\xef\xbb\xbf{\"a\":1}\r\n")
	l := list(t, h)
	if len(l.Items) != 1 || strings.TrimSpace(string(l.Items[0].Body)) != `{"a":1}` {
		t.Errorf("items = %+v", l.Items)
	}
}

func TestFailModeToggle(t *testing.T) {
	h := NewMockReceiver().Handler()
	if code, _ := do(t, h, http.MethodPost, "/fail/on", ""); code != 200 {
		t.Fatalf("fail/on = %d", code)
	}
	for _, p := range []string{"/feishu", "/dingtalk", "/wecom"} {
		if code, _ := do(t, h, http.MethodPost, p, `{}`); code != 500 {
			t.Errorf("%s in fail mode = %d, want 500", p, code)
		}
	}
	if l := list(t, h); !l.Fail || l.Count != 0 {
		t.Errorf("failed pushes must not be recorded: %+v", l)
	}
	do(t, h, http.MethodPost, "/fail/off", "")
	if code, _ := do(t, h, http.MethodPost, "/feishu", `{}`); code != 200 {
		t.Errorf("after fail/off = %d, want 200", code)
	}
	if l := list(t, h); l.Fail || l.Count != 1 {
		t.Errorf("listing = %+v", l)
	}
}

func TestDeleteClears(t *testing.T) {
	h := NewMockReceiver().Handler()
	do(t, h, http.MethodPost, "/feishu", `{}`)
	do(t, h, http.MethodPost, "/wecom", `{}`)
	code, out := do(t, h, http.MethodDelete, "/received", "")
	if code != 200 || !strings.Contains(out, `"cleared":2`) {
		t.Errorf("delete = %d %q", code, out)
	}
	if l := list(t, h); l.Count != 0 || l.Items == nil {
		t.Errorf("listing after clear = %+v", l)
	}
}

func TestWrongMethodsAndUnknownPaths(t *testing.T) {
	h := NewMockReceiver().Handler()
	tests := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/feishu", 405},
		{http.MethodGet, "/fail/on", 405},
		{http.MethodPost, "/received", 405},
		{http.MethodPost, "/nope", 404},
	}
	for _, tt := range tests {
		if code, _ := do(t, h, tt.method, tt.path, ""); code != tt.want {
			t.Errorf("%s %s = %d, want %d", tt.method, tt.path, code, tt.want)
		}
	}
}
