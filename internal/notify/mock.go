// Package notify holds the webhook senders and the mock receiver used to
// verify them without reaching a real Feishu / DingTalk / WeCom endpoint.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

const maxBodyBytes = 1 << 20

// Received is one webhook push recorded by the mock receiver.
type Received struct {
	Seq     int             `json:"seq"`
	Channel string          `json:"channel"`
	Path    string          `json:"path"`
	Time    time.Time       `json:"time"`
	Body    json.RawMessage `json:"body"`
}

// platformReply is the success payload each platform returns on a delivered push.
var platformReply = map[string]string{
	"feishu":   `{"code":0,"msg":"success"}`,
	"dingtalk": `{"errcode":0,"errmsg":"ok"}`,
	"wecom":    `{"errcode":0,"errmsg":"ok"}`,
}

// MockReceiver records webhook pushes in memory and can be switched into a
// mode where every push is answered with HTTP 500.
type MockReceiver struct {
	mu    sync.Mutex
	items []Received
	seq   int
	fail  bool
	now   func() time.Time
}

// NewMockReceiver returns an empty receiver.
func NewMockReceiver() *MockReceiver {
	return &MockReceiver{items: []Received{}, now: time.Now}
}

// Handler returns the HTTP surface of the receiver.
func (m *MockReceiver) Handler() http.Handler {
	mux := http.NewServeMux()
	for channel := range platformReply {
		mux.HandleFunc("/"+channel, m.handlePush(channel))
	}
	mux.HandleFunc("/received", m.handleReceived)
	mux.HandleFunc("/fail/on", m.handleFail(true))
	mux.HandleFunc("/fail/off", m.handleFail(false))
	return mux
}

// Serve serves on ln until ctx is cancelled.
func (m *MockReceiver) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: m.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

// Items returns a copy of everything received so far.
func (m *MockReceiver) Items() []Received {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Received{}, m.items...)
}

func (m *MockReceiver) handlePush(channel string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot read body: " + err.Error()})
			return
		}
		m.mu.Lock()
		if m.fail {
			m.mu.Unlock()
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mock-notify failure mode is on"})
			return
		}
		m.seq++
		m.items = append(m.items, Received{
			Seq:     m.seq,
			Channel: channel,
			Path:    r.URL.Path,
			Time:    m.now(),
			Body:    asJSON(body),
		})
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(platformReply[channel]))
	}
}

func (m *MockReceiver) handleReceived(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		m.mu.Lock()
		out := struct {
			Fail  bool       `json:"fail"`
			Count int        `json:"count"`
			Items []Received `json:"items"`
		}{m.fail, len(m.items), append([]Received{}, m.items...)}
		m.mu.Unlock()
		writeJSON(w, http.StatusOK, out)
	case http.MethodDelete:
		m.mu.Lock()
		n := len(m.items)
		m.items = []Received{}
		m.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]int{"cleared": n})
	default:
		methodNotAllowed(w, "GET, DELETE")
	}
}

func (m *MockReceiver) handleFail(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		m.mu.Lock()
		m.fail = on
		m.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]bool{"fail": on})
	}
}

// asJSON keeps valid JSON bodies as-is and wraps anything else as a JSON string,
// so the /received listing is always well-formed.
func asJSON(body []byte) json.RawMessage {
	// Windows PowerShell writes a UTF-8 BOM into files that curl then uploads verbatim.
	body = bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))
	if json.Valid(body) {
		return json.RawMessage(append([]byte{}, body...))
	}
	quoted, _ := json.Marshal(string(body))
	return quoted
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
