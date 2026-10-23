package mock

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// Wire types reproducing the Prometheus HTTP API JSON encoding.

type apiResponse struct {
	Status    string   `json:"status"`
	Data      any      `json:"data,omitempty"`
	ErrorType string   `json:"errorType,omitempty"`
	Error     string   `json:"error,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

type queryData struct {
	ResultType string `json:"resultType"`
	Result     any    `json:"result"`
}

type vectorSample struct {
	Metric map[string]string `json:"metric"`
	Value  samplePair        `json:"value"`
}

type matrixSeries struct {
	Metric map[string]string `json:"metric"`
	Values []samplePair      `json:"values"`
}

// samplePair encodes as [<unix seconds float>, "<value>"].
type samplePair struct {
	ts int64 // milliseconds
	v  string
}

func floatPair(ts int64, v float64) samplePair { return samplePair{ts, formatFloat(v)} }

func (p samplePair) MarshalJSON() ([]byte, error) {
	val, err := json.Marshal(p.v)
	if err != nil {
		return nil, err
	}
	b := make([]byte, 0, 32+len(val))
	b = append(b, '[')
	b = append(b, formatTimestamp(p.ts)...)
	b = append(b, ',')
	b = append(b, val...)
	return append(b, ']'), nil
}

func formatFloat(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// formatTimestamp renders milliseconds as seconds with up to 3 decimals.
func formatTimestamp(ms int64) string {
	sign := ""
	if ms < 0 {
		sign, ms = "-", -ms
	}
	s := sign + strconv.FormatInt(ms/1000, 10)
	if frac := ms % 1000; frac != 0 {
		s += "." + strings.TrimRight(leftPad3(frac), "0")
	}
	return s
}

func leftPad3(n int64) string {
	s := strconv.FormatInt(n, 10)
	return strings.Repeat("0", 3-len(s)) + s
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeData(w http.ResponseWriter, data any, warnings []string) {
	writeJSON(w, http.StatusOK, apiResponse{Status: "success", Data: data, Warnings: warnings})
}

func writeError(w http.ResponseWriter, code int, errType, msg string) {
	writeJSON(w, code, apiResponse{Status: "error", ErrorType: errType, Error: msg})
}
