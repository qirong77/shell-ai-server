package main

import (
	"net/http"
)

func handleAPIDocs(w http.ResponseWriter, r *http.Request) {
	sendJSON(w, 200, ok(apiDocs(), nil))
}

func getApiSummary() []string {
	return []string{
		"GET  /                     — Server info & API summary",
		"GET  /health               — Health check",
		"GET  /api                  — Full API documentation (JSON)",
		"",
		"Shell:",
		"POST /shell/exec           — Execute command (sync, with timeout)",
		"POST /shell/spawn          — Start background task (async)",
		"GET  /shell/tasks          — List background tasks",
		"GET  /shell/tasks/:id      — Get task details + log",
		"GET  /shell/tasks/:id/log  — Get task log (plain text)",
		"POST /shell/tasks/:id/kill — Kill a background task",
		"DELETE /shell/tasks/:id    — Remove completed task",
		"POST /shell/script         — Execute multi-line script",
		"",
		"Files:",
		"GET  /files?path=          — Read file or list directory",
		"POST /files                — Write/create file",
		"POST /files/mkdir          — Create directory",
		"DELETE /files?path=        — Delete file or directory",
		"POST /files/move           — Move/rename file",
		"GET  /files/stat?path=     — Get file stats",
		"POST /files/search         — Search file contents (grep)",
		"",
		"Processes:",
		"GET  /processes            — List running processes",
		"POST /processes/:pid/kill  — Kill process by PID",
		"",
		"System:",
		"GET  /system               — Full system information",
		"GET  /system/ports         — List listening ports",
		"GET  /system/disk          — Disk usage",
		"",
		"Services:",
		"POST /services             — Register a service",
		"GET  /services             — Query services (filters: name,type,status,tag,port,pid,host,q)",
		"GET  /services/:id         — Get service by ID",
		"PUT  /services/:id         — Update service record",
		"DELETE /services/:id       — Delete service record",
		"POST /services/:id/start   — Start a registered service",
		"POST /services/:id/stop    — Stop a registered service",
		"GET  /services/:id/health          — Health check a service",
		"GET  /services/:id/health-history  — Health check history",
		"GET  /services/monitor/status      — Overall monitoring status",
		"DELETE /services           — Clear all services",
		"",
		"Network:",
		"GET  /net/port/:port       — Check if port is open",
		"POST /net/http             — HTTP proxy request",
		"GET  /net/dns?domain=      — DNS lookup",
		"",
		"Environment:",
		"GET  /env                  — Get environment variables",
		"POST /env                  — Set environment variable",
		"",
		"Batch:",
		"POST /batch                — Execute multiple operations",
	}
}

func apiDocs() map[string]interface{} {
	ep := func(method, path, summary string, params map[string]interface{}, req interface{}, resp interface{}) map[string]interface{} {
		m := map[string]interface{}{
			"method":  method,
			"path":    path,
			"summary": summary,
		}
		if params != nil {
			m["params"] = params
		}
		if req != nil {
			m["requestExample"] = req
		}
		if resp != nil {
			m["responseExample"] = resp
		}
		return m
	}

	return map[string]interface{}{
		"title":       "Shell-AI-Server API Documentation",
		"version":     "1.0.0",
		"description": "A server-side shell server for LLM clients. No authentication required. All endpoints accept and return JSON with a standard envelope. Base URL: http://<host>:9100",
		"standardResponse": map[string]interface{}{
			"description": "Every API response uses this envelope",
			"success":     map[string]interface{}{"ok": true, "data": map[string]interface{}{}, "meta": map[string]interface{}{"timestamp": "ISO-8601"}},
			"error":       map[string]interface{}{"ok": false, "error": map[string]interface{}{"code": "ERROR_CODE", "message": "description"}, "meta": map[string]interface{}{"timestamp": "ISO-8601"}},
		},
		"categories": []map[string]interface{}{
			{
				"name":        "Shell Execution",
				"description": "Execute shell commands on the host. Supports synchronous execution with timeout, background tasks with log tracking, and multi-line scripts.",
				"endpoints": []map[string]interface{}{
					ep("POST", "/shell/exec", "Execute a command synchronously and return stdout/stderr",
						map[string]interface{}{"command": "string (required) — the shell command to execute", "cwd": "string — working directory", "timeout": "number — max execution time in ms", "env": "object — extra environment variables"},
						map[string]interface{}{"command": "echo hello && whoami", "cwd": "/tmp", "timeout": 10000},
						map[string]interface{}{"ok": true, "data": map[string]interface{}{"exitCode": 0, "stdout": "hello\n", "stderr": "", "timedOut": false}}),
					ep("POST", "/shell/spawn", "Start a long-running background task",
						map[string]interface{}{"command": "string (required)", "args": "string[]", "cwd": "string", "env": "object", "shell": "boolean"},
						map[string]interface{}{"command": "node", "args": []string{"server.js"}, "cwd": "/myapp"},
						map[string]interface{}{"ok": true, "data": map[string]interface{}{"id": "task-uuid", "pid": 12345, "status": "running", "logPath": "data/logs/task-uuid.log"}}),
					ep("GET", "/shell/tasks", "List all background tasks", map[string]interface{}{"status": "query param — running/completed/killed/error"}, nil, nil),
					ep("GET", "/shell/tasks/:id", "Get task details + recent log", map[string]interface{}{"id": "path param", "lines": "query param"}, nil, nil),
					ep("GET", "/shell/tasks/:id/log", "Get task log as plain text", map[string]interface{}{"id": "path param", "lines": "query param"}, nil, nil),
					ep("POST", "/shell/tasks/:id/kill", "Send a signal to terminate a background task", map[string]interface{}{"id": "path param", "signal": "body field"}, map[string]interface{}{"signal": "SIGKILL"}, nil),
					ep("DELETE", "/shell/tasks/:id", "Remove a completed task from tracking", map[string]interface{}{"id": "path param"}, nil, nil),
					ep("POST", "/shell/script", "Execute a multi-line shell script", map[string]interface{}{"script": "string (required)", "cwd": "string", "timeout": "number"}, map[string]interface{}{"script": "cd /tmp\necho hi", "cwd": "/tmp"}, nil),
				},
			},
			{
				"name":        "File Operations",
				"description": "Read, write, move, delete files and directories. Search file contents with regex.",
				"endpoints": []map[string]interface{}{
					ep("GET", "/files?path=", "Read file contents or list directory", map[string]interface{}{"path": "query param (required)", "encoding": "query param"}, nil, nil),
					ep("POST", "/files", "Write content to a file", map[string]interface{}{"path": "string (required)", "content": "string (required)", "append": "boolean"}, map[string]interface{}{"path": "/app/config.json", "content": "{}"}, nil),
					ep("POST", "/files/mkdir", "Create a directory (recursive)", map[string]interface{}{"path": "string (required)"}, map[string]interface{}{"path": "/app/logs/2024"}, nil),
					ep("DELETE", "/files?path=", "Delete a file or directory", map[string]interface{}{"path": "query param (required)"}, nil, nil),
					ep("POST", "/files/move", "Move or rename a file/directory", map[string]interface{}{"from": "string (required)", "to": "string (required)"}, map[string]interface{}{"from": "/app/old.json", "to": "/app/new.json"}, nil),
					ep("GET", "/files/stat?path=", "Get file metadata", map[string]interface{}{"path": "query param (required)"}, nil, nil),
					ep("POST", "/files/search", "Search file contents with regex (grep)", map[string]interface{}{"pattern": "string (required)", "path": "string", "recursive": "boolean", "maxResults": "number"}, map[string]interface{}{"pattern": "TODO|FIXME", "path": "./src", "maxResults": 20}, nil),
				},
			},
			{
				"name":        "Process Management",
				"description": "List and kill processes running on the host.",
				"endpoints": []map[string]interface{}{
					ep("GET", "/processes", "List running processes (ps)", map[string]interface{}{"filter": "query param"}, nil, nil),
					ep("POST", "/processes/:pid/kill", "Send a signal to a process by PID", map[string]interface{}{"pid": "path param (required)", "signal": "body field"}, map[string]interface{}{"signal": "SIGTERM"}, nil),
				},
			},
			{
				"name":        "System Information",
				"description": "Inspect host system: CPU, memory, disk, network, listening ports.",
				"endpoints": []map[string]interface{}{
					ep("GET", "/system", "Full system information", nil, nil, nil),
					ep("GET", "/system/ports", "List all listening TCP ports", nil, nil, nil),
					ep("GET", "/system/disk", "Disk usage information", nil, nil, nil),
				},
			},
			{
				"name":        "Service Registry",
				"description": "Register, query, update, and manage services deployed by the LLM.",
				"standardRecord": map[string]interface{}{
					"fields": map[string]interface{}{
						"id": "string — auto-generated UUID", "name": "string — service name", "type": "string — service type",
						"port": "number | null", "pid": "number | null", "host": "string", "status": "string",
						"healthCheck": "object", "tags": "string[]", "config": "object", "metadata": "object",
						"workingDir": "string | null", "startCommand": "string | null", "stopCommand": "string | null", "logPath": "string | null",
						"deployedAt": "ISO 8601", "updatedAt": "ISO 8601",
					},
				},
				"endpoints": []map[string]interface{}{
					ep("POST", "/services", "Register a new service", nil, map[string]interface{}{"name": "my-api", "type": "api", "port": 3000, "status": "running"}, nil),
					ep("GET", "/services", "Query services with flexible filters", map[string]interface{}{"name": "string", "type": "string", "status": "string", "tag": "string", "q": "string"}, nil, nil),
					ep("GET", "/services/:id", "Get a single service record", map[string]interface{}{"id": "path param"}, nil, nil),
					ep("PUT", "/services/:id", "Update a service record", map[string]interface{}{"id": "path param"}, nil, nil),
					ep("DELETE", "/services/:id", "Delete a service from the registry", map[string]interface{}{"id": "path param"}, nil, nil),
					ep("POST", "/services/:id/start", "Start a registered service", map[string]interface{}{"command": "body", "cwd": "body"}, nil, nil),
					ep("POST", "/services/:id/stop", "Stop a registered service", map[string]interface{}{"signal": "body"}, nil, nil),
					ep("GET", "/services/:id/health", "Run health checks on a service", map[string]interface{}{"id": "path param"}, nil, nil),
					ep("GET", "/services/:id/health-history", "Get health check history", map[string]interface{}{"id": "path param"}, nil, nil),
					ep("GET", "/services/monitor/status", "Overall monitoring status", nil, nil, nil),
					ep("DELETE", "/services", "Clear all service records", nil, nil, nil),
				},
			},
			{
				"name":        "Network Tools",
				"description": "Port checking, HTTP proxy requests, and DNS lookups.",
				"endpoints": []map[string]interface{}{
					ep("GET", "/net/port/:port", "Check if a TCP port is open", map[string]interface{}{"port": "path param (required)", "host": "query param"}, nil, nil),
					ep("POST", "/net/http", "Make an outbound HTTP request through the server", map[string]interface{}{"url": "string (required)", "method": "string", "headers": "object", "body": "string|object", "timeout": "number"}, map[string]interface{}{"url": "https://api.github.com", "method": "GET"}, nil),
					ep("GET", "/net/dns?domain=", "DNS lookup for a domain", map[string]interface{}{"domain": "query param (required)"}, nil, nil),
				},
			},
			{
				"name":        "Environment Variables",
				"description": "Read and set environment variables for the server process and spawned children.",
				"endpoints": []map[string]interface{}{
					ep("GET", "/env", "Get all environment variables or a specific one", map[string]interface{}{"key": "query param"}, nil, nil),
					ep("POST", "/env", "Set an environment variable", map[string]interface{}{"key": "string (required)", "value": "string"}, map[string]interface{}{"key": "NODE_ENV", "value": "production"}, nil),
				},
			},
			{
				"name":        "Batch Operations",
				"description": "Execute multiple operations in a single request.",
				"endpoints": []map[string]interface{}{
					ep("POST", "/batch", "Execute a sequence of operations", map[string]interface{}{"operations": "array of { type, ...fields }"}, map[string]interface{}{"operations": []map[string]interface{}{{"type": "mkdir", "path": "/app/data"}, {"type": "write", "path": "/app/data/config.json", "content": "{}"}}}, nil),
				},
			},
		},
	}
}
