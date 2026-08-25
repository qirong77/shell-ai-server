package main

import (
	"context"
	"net/http"
	"time"
)

// ShellExecResult is the result of a synchronous /shell/exec call.
type ShellExecResult struct {
	Command  string `json:"command"`
	Cwd      string `json:"cwd"`
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	TimedOut bool   `json:"timedOut"`
	Duration int64  `json:"duration"`
	ErrText  string `json:"error,omitempty"`
}

func handleShellExec(w http.ResponseWriter, r *http.Request) {
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
	command := stringField(obj, "command")
	if command == "" {
		sendJSON(w, 400, fail("MISSING_PARAM", "Missing 'command' field", nil, nil))
		return
	}
	cwd := stringField(obj, "cwd")
	if cwd == "" {
		cwd = serverCwd
	}
	timeout := boundedInt(obj["timeout"], 30000, 1, 10*60*1000)
	var env map[string]interface{}
	if e, ok := obj["env"].(map[string]interface{}); ok {
		env = e
	}

	start := time.Now()
	result := execCommandCtx(command, cwd, timeout, env)
	result.Duration = time.Since(start).Milliseconds()
	result.Command = command
	result.Cwd = cwd

	sendJSON(w, 200, ok(result, map[string]interface{}{"command": command}))
}

func execCommandCtx(command, cwd string, timeoutMS int, env map[string]interface{}) ShellExecResult {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()

	cmd := prepareCommand(command, cwd, env)
	out := &ShellExecResult{ExitCode: -1}

	var stdoutBuf, stderrBuf limitedBuffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run() // waits for completion or context timeout
	out.Stdout = stdoutBuf.String()
	out.Stderr = stderrBuf.String()

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			out.TimedOut = true
			out.ExitCode = -1
		} else if exErr, ok := err.(interface{ ExitCode() int }); ok {
			out.ExitCode = exErr.ExitCode()
			out.ErrText = err.Error()
		} else {
			out.ExitCode = -1
			out.ErrText = err.Error()
		}
	} else {
		out.ExitCode = 0
	}
	return *out
}

func handleShellSpawn(w http.ResponseWriter, r *http.Request) {
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
	command := stringField(obj, "command")
	if command == "" {
		sendJSON(w, 400, fail("MISSING_PARAM", "Missing 'command' field", nil, nil))
		return
	}
	var args []string
	if a, ok := obj["args"].([]interface{}); ok {
		for _, x := range a {
			if s, ok2 := x.(string); ok2 {
				args = append(args, s)
			}
		}
	}
	opts := map[string]interface{}{}
	if v, ok := obj["cwd"].(string); ok && v != "" {
		opts["cwd"] = v
	}
	if v, ok := obj["env"].(map[string]interface{}); ok {
		opts["env"] = v
	}
	if v, ok := obj["shell"].(bool); ok {
		opts["shell"] = v
	}
	task := taskManager.Start(command, args, opts)
	sendJSON(w, 200, ok(task, map[string]interface{}{"taskId": task.ID}))
}

func handleShellTasks(w http.ResponseWriter, r *http.Request) {
	status := queryStr(r, "status")
	tasks := taskManager.List()
	if status != "" {
		filtered := tasks[:0]
		for _, t := range tasks {
			if t.Status == status {
				filtered = append(filtered, t)
			}
		}
		tasks = filtered
	}
	sendJSON(w, 200, ok(tasks, map[string]interface{}{"count": len(tasks)}))
}

func handleShellTaskGet(w http.ResponseWriter, r *http.Request, params map[string]string) {
	id := params["id"]
	task := taskManager.Get(id)
	if task == nil {
		sendJSON(w, 404, fail("NOT_FOUND", "Task not found", nil, nil))
		return
	}
	lines := queryInt(r, "lines", 100, 1, 5000)
	log := taskManager.GetLog(id, lines)
	// Flatten the task fields to the top level, with `log` appended, to match
	// the Node implementation ({ ...task, log }).
	data := map[string]interface{}{
		"id":        task.ID,
		"pid":       task.PID,
		"command":   task.Command,
		"args":      task.Args,
		"status":    task.Status,
		"startedAt": task.StartedAt,
		"endedAt":   task.EndedAt,
		"exitCode":  task.ExitCode,
		"signal":    task.Signal,
		"logPath":   task.LogPath,
		"cwd":       task.Cwd,
		"log":       log,
	}
	if task.ErrText != "" {
		data["error"] = task.ErrText
	}
	sendJSON(w, 200, ok(data, nil))
}

func handleShellTaskLog(w http.ResponseWriter, r *http.Request, params map[string]string) {
	id := params["id"]
	task := taskManager.Get(id)
	if task == nil {
		sendJSON(w, 404, fail("NOT_FOUND", "Task not found", nil, nil))
		return
	}
	lines := queryInt(r, "lines", 200, 1, 5000)
	log := taskManager.GetLog(id, lines)
	if log == "" {
		log = "(no log output)"
	}
	sendText(w, 200, log)
}

func handleShellTaskKill(w http.ResponseWriter, r *http.Request, params map[string]string) {
	id := params["id"]
	body, err := readBody(r)
	if err != nil {
		sendJSON(w, 400, fail("BAD_REQUEST", err.Error(), nil, nil))
		return
	}
	obj, _ := parseJSONBody(body)
	signal := stringField(obj, "signal")
	if signal == "" {
		signal = "SIGTERM"
	}
	if !taskManager.Kill(id, signal) {
		sendJSON(w, 404, fail("NOT_FOUND", "Task not found or already stopped", nil, nil))
		return
	}
	sendJSON(w, 200, ok(map[string]interface{}{"killed": true, "taskId": id, "signal": signal}, nil))
}

func handleShellTaskDelete(w http.ResponseWriter, r *http.Request, params map[string]string) {
	id := params["id"]
	task := taskManager.Get(id)
	if task == nil {
		sendJSON(w, 404, fail("NOT_FOUND", "Task not found", nil, nil))
		return
	}
	if task.Status == "running" {
		sendJSON(w, 400, fail("STILL_RUNNING", "Task is still running, kill it first", nil, nil))
		return
	}
	if !taskManager.Delete(id) {
		sendJSON(w, 404, fail("NOT_FOUND", "Task not found", nil, nil))
		return
	}
	sendJSON(w, 200, ok(map[string]interface{}{"deleted": true, "taskId": id}, nil))
}

func handleShellScript(w http.ResponseWriter, r *http.Request) {
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
	script := stringField(obj, "script")
	if script == "" {
		sendJSON(w, 400, fail("MISSING_PARAM", "Missing 'script' field", nil, nil))
		return
	}
	cwd := stringField(obj, "cwd")
	if cwd == "" {
		cwd = serverCwd
	}
	timeout := boundedInt(obj["timeout"], 60000, 1, 10*60*1000)
	useShell := stringField(obj, "shell")
	if useShell == "" {
		useShell = "/bin/bash"
	}

	result := execScript(script, useShell, cwd, timeout, obj["env"])

	sendJSON(w, 200, ok(map[string]interface{}{
		"script":   script,
		"shell":    useShell,
		"exitCode": result.ExitCode,
		"stdout":   result.Stdout,
		"stderr":   result.Stderr,
		"timedOut": result.TimedOut,
		"error":    result.ErrText,
	}, nil))
}
