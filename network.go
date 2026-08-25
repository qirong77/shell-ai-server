package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func handleNetPort(w http.ResponseWriter, r *http.Request, params map[string]string) {
	port := toInt(params["port"])
	host := queryStr(r, "host")
	if host == "" {
		host = "localhost"
	}
	open := checkPort(host, port)
	sendJSON(w, 200, ok(map[string]interface{}{"host": host, "port": port, "open": open}, nil))
}

func handleNetHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		sendJSON(w, 400, fail("BAD_REQUEST", err.Error(), nil, nil))
		return
	}
	obj, err := parseJSONBody(body)
	if err != nil {
		sendJSON(w, 400, fail("BAD_REQUEST", err.Error(), nil, nil))
		return
	}
	url := stringField(obj, "url")
	if url == "" {
		sendJSON(w, 400, fail("MISSING_PARAM", "Missing 'url' field", nil, nil))
		return
	}
	method := stringField(obj, "method")
	if method == "" {
		method = "GET"
	}
	timeout := boundedInt(obj["timeout"], 10000, 1, 10*60*1000)

	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		sendJSON(w, 400, fail("BAD_REQUEST", "Invalid URL: "+err.Error(), nil, nil))
		return
	}
	if headers, ok := obj["headers"].(map[string]interface{}); ok {
		for k, v := range headers {
			req.Header.Set(k, anyString(v))
		}
	}
	if reqBody, ok := obj["body"]; ok && method != "GET" {
		var bodyReader io.Reader
		switch b := reqBody.(type) {
		case string:
			bodyReader = strings.NewReader(b)
		default:
			bodyReader = bytes.NewReader([]byte(anyString(b)))
		}
		req.Body = io.NopCloser(bodyReader)
	}

	client := &http.Client{Timeout: time.Duration(timeout) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		sendJSON(w, 502, fail("HTTP_ERROR", "HTTP request failed: "+err.Error(), nil, nil))
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))

	headers := map[string]interface{}{}
	for k, vs := range resp.Header {
		if len(vs) > 0 {
			headers[k] = vs[0]
		}
	}
	sendJSON(w, 200, ok(map[string]interface{}{
		"status":     resp.StatusCode,
		"statusText": resp.Status,
		"headers":    headers,
		"body":       string(respBody),
	}, nil))
}

func handleNetDNS(w http.ResponseWriter, r *http.Request) {
	domain := queryStr(r, "domain")
	if domain == "" {
		sendJSON(w, 400, fail("MISSING_PARAM", "Missing 'domain' query param", nil, nil))
		return
	}
	addrs, err := net.LookupHost(domain)
	addresses := []map[string]interface{}{}
	for _, a := range addrs {
		addresses = append(addresses, map[string]interface{}{"address": a, "family": dnsFamily(a)})
	}
	if err != nil {
		sendJSON(w, 500, fail("DNS_ERROR", "DNS lookup failed: "+err.Error(), nil, nil))
		return
	}
	sendJSON(w, 200, ok(map[string]interface{}{"domain": domain, "addresses": addresses}, nil))
}

func dnsFamily(addr string) int {
	ip := net.ParseIP(addr)
	if ip != nil && ip.To4() != nil {
		return 4
	}
	return 6
}

func handleBatch(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		sendJSON(w, 400, fail("BAD_REQUEST", err.Error(), nil, nil))
		return
	}
	obj, err := parseJSONBody(body)
	if err != nil {
		sendJSON(w, 400, fail("BAD_REQUEST", err.Error(), nil, nil))
		return
	}
	ops, isOps := obj["operations"].([]interface{})
	if !isOps {
		sendJSON(w, 400, fail("INVALID_PARAM", "'operations' must be an array", nil, nil))
		return
	}
	results := make([]interface{}, 0, len(ops))
	for _, rawOp := range ops {
		op, _ := rawOp.(map[string]interface{})
		opType := stringField(op, "type")
		item := map[string]interface{}{"type": opType}
		command := stringField(op, "command")
		cwd := stringField(op, "cwd")
		if cwd == "" {
			cwd = serverCwd
		}
		var env map[string]interface{}
		if e, ok := op["env"].(map[string]interface{}); ok {
			env = e
		}
		filePath := stringField(op, "path")
		switch opType {
		case "exec":
			res := execCommandCtx(command, cwd, 30000, env)
			item["result"] = res
			item["ok"] = true
		case "spawn":
			var args []string
			if a, ok := op["args"].([]interface{}); ok {
				for _, x := range a {
					if s, ok2 := x.(string); ok2 {
						args = append(args, s)
					}
				}
			}
			task := taskManager.Start(command, args, map[string]interface{}{"cwd": cwd, "env": env})
			item["result"] = task
			item["ok"] = true
		case "read":
			data, err := os.ReadFile(resolvePath(filePath))
			if err != nil {
				item["ok"] = false
				item["error"] = err.Error()
			} else {
				item["result"] = string(data)
				item["ok"] = true
			}
		case "write":
			content := anyString(op["content"])
			if err := os.WriteFile(resolvePath(filePath), []byte(content), 0644); err != nil {
				item["ok"] = false
				item["error"] = err.Error()
			} else {
				item["result"] = map[string]interface{}{"written": true}
				item["ok"] = true
			}
		case "mkdir":
			if err := os.MkdirAll(resolvePath(filePath), 0755); err != nil {
				item["ok"] = false
				item["error"] = err.Error()
			} else {
				item["result"] = map[string]interface{}{"created": true}
				item["ok"] = true
			}
		default:
			item["ok"] = false
			item["result"] = map[string]interface{}{"error": "Unknown operation type: " + opType}
		}
		results = append(results, item)
	}
	sendJSON(w, 200, ok(results, map[string]interface{}{"count": len(results)}))
}
