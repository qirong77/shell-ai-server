package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxBodySize = 10 * 1024 * 1024 // 10 MB
)

// Envelope is the standard response format returned by every endpoint so that
// LLM clients can parse results uniformly regardless of which endpoint was hit.
type Envelope struct {
	OK   bool        `json:"ok"`
	Data interface{} `json:"data,omitempty"`
	// Error is set only on failure.
	Error *EnvelopeError         `json:"error,omitempty"`
	Meta  map[string]interface{} `json:"meta,omitempty"`
}

type EnvelopeError struct {
	Code    string      `json:"code"`
	Message interface{} `json:"message"`
	Details interface{} `json:"details,omitempty"`
}

func ok(data interface{}, meta map[string]interface{}) Envelope {
	if meta == nil {
		meta = map[string]interface{}{}
	}
	meta["timestamp"] = time.Now().UTC().Format(time.RFC3339)
	return Envelope{OK: true, Data: data, Meta: meta}
}

func fail(code string, message interface{}, details interface{}, meta map[string]interface{}) Envelope {
	if meta == nil {
		meta = map[string]interface{}{}
	}
	meta["timestamp"] = time.Now().UTC().Format(time.RFC3339)
	return Envelope{
		OK: false,
		Error: &EnvelopeError{
			Code:    code,
			Message: message,
			Details: details,
		},
		Meta: meta,
	}
}

func sendJSON(w http.ResponseWriter, status int, obj interface{}) {
	body, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		body = []byte(`{"ok":false,"error":{"code":"ENCODE_ERROR","message":"failed to encode response"}}`)
		status = 500
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	w.WriteHeader(status)
	w.Write(body)
}

func sendText(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	io.WriteString(w, text)
}

// readBody reads and returns the raw request body, enforcing the size cap.
func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBodySize {
		return nil, errors.New("Request body too large")
	}
	return body, nil
}

func parseJSONBody(buf []byte) (map[string]interface{}, error) {
	if len(buf) == 0 {
		return map[string]interface{}{}, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(buf)))
	dec.UseNumber()
	var obj map[string]interface{}
	if err := dec.Decode(&obj); err != nil {
		return nil, errors.New("Invalid JSON body")
	}
	return obj, nil
}

// boundedInt coerces a value to an integer within [min,max]. If the value is
// not a valid integer, fallback is returned.
func boundedInt(v interface{}, fallback, min, max int) int {
	var n int64
	switch val := v.(type) {
	case float64:
		n = int64(val)
	case json.Number:
		i, err := val.Int64()
		if err != nil {
			return fallback
		}
		n = i
	case int:
		return clampInt(val, min, max)
	case int64:
		n = val
	default:
		return fallback
	}
	return clampInt(int(n), min, max)
}

func clampInt(n, min, max int) int {
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

func queryInt(r *http.Request, key string, fallback, min, max int) int {
	return boundedInt(r.URL.Query().Get(key), fallback, min, max)
}

func queryStr(r *http.Request, key string) string {
	return r.URL.Query().Get(key)
}

// itoa is a thin wrapper over strconv.Itoa used throughout the codebase.
func itoa(n int) string {
	return strconv.Itoa(n)
}

func itoa64(n int64) string {
	return strconv.FormatInt(n, 10)
}
