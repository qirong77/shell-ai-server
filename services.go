package main

import (
	"net/http"
)

func handleServiceRegister(w http.ResponseWriter, r *http.Request) {
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
	record := registry.Register(obj)
	healthMonitor.OnServiceChanged(record.ID)
	sendJSON(w, 201, ok(record, map[string]interface{}{"serviceId": record.ID}))
}

func handleServiceQuery(w http.ResponseWriter, r *http.Request) {
	filters := map[string]interface{}{}
	for _, key := range []string{"name", "type", "status", "tag", "port", "pid", "host", "q"} {
		if v := queryStr(r, key); v != "" {
			filters[key] = v
		}
	}
	results := registry.Query(filters)
	sendJSON(w, 200, ok(results, map[string]interface{}{"count": len(results), "filters": filters}))
}

func handleServiceGet(w http.ResponseWriter, r *http.Request, params map[string]string) {
	record := registry.Get(params["id"])
	if record == nil {
		sendJSON(w, 404, fail("NOT_FOUND", "Service not found", nil, nil))
		return
	}
	sendJSON(w, 200, ok(record, nil))
}

func handleServiceUpdate(w http.ResponseWriter, r *http.Request, params map[string]string) {
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
	updated := registry.Update(params["id"], obj)
	if updated == nil {
		sendJSON(w, 404, fail("NOT_FOUND", "Service not found", nil, nil))
		return
	}
	healthMonitor.OnServiceChanged(params["id"])
	sendJSON(w, 200, ok(updated, nil))
}

func handleServiceDelete(w http.ResponseWriter, r *http.Request, params map[string]string) {
	id := params["id"]
	if !registry.Delete(id) {
		sendJSON(w, 404, fail("NOT_FOUND", "Service not found", nil, nil))
		return
	}
	healthMonitor.Remove(id)
	sendJSON(w, 200, ok(map[string]interface{}{"deleted": true, "id": id}, nil))
}

func handleServiceStart(w http.ResponseWriter, r *http.Request, params map[string]string) {
	id := params["id"]
	record := registry.Get(id)
	if record == nil {
		sendJSON(w, 404, fail("NOT_FOUND", "Service not found", nil, nil))
		return
	}
	body, err := readBody(r)
	if err != nil {
		sendJSON(w, 400, fail("BAD_REQUEST", err.Error(), nil, nil))
		return
	}
	obj, _ := parseJSONBody(body)

	var task *Task
	cmd := stringField(obj, "command")
	if cmd == "" && record.StartCommand != nil {
		cmd = *record.StartCommand
	}
	if cmd != "" {
		opts := map[string]interface{}{"shell": true}
		if cwd := stringField(obj, "cwd"); cwd != "" {
			opts["cwd"] = cwd
		} else if record.WorkingDir != nil {
			opts["cwd"] = *record.WorkingDir
		}
		task = taskManager.Start(cmd, nil, opts)
		pid := task.PID
		metadata := cloneMap(record.Metadata)
		metadata["taskId"] = task.ID
		registry.Update(id, map[string]interface{}{"pid": pid, "status": "running", "metadata": metadata})
	} else {
		registry.Update(id, map[string]interface{}{"status": "running"})
	}
	updated := registry.Get(id)
	sendJSON(w, 200, ok(map[string]interface{}{"service": updated, "task": task}, nil))
}

func handleServiceStop(w http.ResponseWriter, r *http.Request, params map[string]string) {
	id := params["id"]
	record := registry.Get(id)
	if record == nil {
		sendJSON(w, 404, fail("NOT_FOUND", "Service not found", nil, nil))
		return
	}
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
	killed := false
	if record.PID != nil && *record.PID > 0 {
		if err := signalPID(*record.PID, signal); err == nil {
			killed = true
		}
	}
	if taskID, ok := record.Metadata["taskId"]; ok {
		if s, ok2 := taskID.(string); ok2 {
			taskManager.Kill(s, signal)
		}
	}
	registry.Update(id, map[string]interface{}{"status": "stopped", "pid": nil})
	updated := registry.Get(id)
	sendJSON(w, 200, ok(map[string]interface{}{"service": updated, "killed": killed}, nil))
}

func handleServiceHealth(w http.ResponseWriter, r *http.Request, params map[string]string) {
	id := params["id"]
	record := registry.Get(id)
	if record == nil {
		sendJSON(w, 404, fail("NOT_FOUND", "Service not found", nil, nil))
		return
	}
	healthMonitor.Check(id)
	// After the synchronous check, fetch the updated record & its history.
	rem := registry.Get(id)
	checks := CheckResult{}
	if rem.PID != nil {
		checks.PIDAlive = pidAlive(*rem.PID)
	}
	if rem.Port != nil {
		checks.PortOpen = checkPort(hostOf(rem), *rem.Port)
	}
	if rem.HealthCheck != nil && rem.HealthCheck.URL != nil {
		checks.HTTPOk = checkHTTP(*rem.HealthCheck.URL)
	}
	healthy := checks.PIDAlive || checks.PortOpen || (checks.HTTPOk != nil && checks.HTTPOk.OK)
	sendJSON(w, 200, ok(map[string]interface{}{"service": rem, "checks": checks, "healthy": healthy}, nil))
}

func handleServiceHealthHistory(w http.ResponseWriter, r *http.Request, params map[string]string) {
	id := params["id"]
	record := registry.Get(id)
	if record == nil {
		sendJSON(w, 404, fail("NOT_FOUND", "Service not found", nil, nil))
		return
	}
	history := healthMonitor.GetHistory(id)
	interval := 30
	if record.HealthCheck != nil && record.HealthCheck.Interval > 0 {
		interval = record.HealthCheck.Interval
	}
	if interval < 10 {
		interval = 10
	}
	sendJSON(w, 200, ok(map[string]interface{}{
		"serviceId":     id,
		"serviceName":   record.Name,
		"currentStatus": record.Status,
		"monitoring":    healthMonitor.IsMonitoring(id),
		"intervalSec":   interval,
		"history":       history,
	}, map[string]interface{}{"count": len(history)}))
}

func handleServiceMonitorStatus(w http.ResponseWriter, r *http.Request) {
	services := registry.Query(nil)
	summary := make([]map[string]interface{}, 0, len(services))
	for _, s := range services {
		hist := healthMonitor.GetHistory(s.ID)
		var last *HistoryEntry
		if len(hist) > 0 {
			last = &hist[len(hist)-1]
		}
		interval := 30
		if s.HealthCheck != nil && s.HealthCheck.Interval > 0 {
			interval = s.HealthCheck.Interval
		}
		if interval < 10 {
			interval = 10
		}
		lastCheck := ""
		lastHealthStatus := ""
		if last != nil {
			lastCheck = last.Time
			if last.Healthy {
				lastHealthStatus = "running"
			} else {
				lastHealthStatus = "error"
			}
		} else if s.Metadata != nil {
			if v, ok := s.Metadata["lastHealthCheck"].(string); ok {
				lastCheck = v
			}
			if v, ok := s.Metadata["lastHealthStatus"].(string); ok {
				lastHealthStatus = v
			}
		}
		summary = append(summary, map[string]interface{}{
			"id":               s.ID,
			"name":             s.Name,
			"status":           s.Status,
			"monitoring":       healthMonitor.IsMonitoring(s.ID),
			"intervalSec":      interval,
			"lastCheck":        lastCheck,
			"lastHealthStatus": lastHealthStatus,
			"checksCount":      len(hist),
		})
	}
	monitoring := 0
	healthy := 0
	errored := 0
	for _, s := range summary {
		if s["monitoring"].(bool) {
			monitoring++
		}
		if s["status"] == "running" {
			healthy++
		}
		if s["status"] == "error" {
			errored++
		}
	}
	sendJSON(w, 200, ok(map[string]interface{}{
		"monitoring": monitoring,
		"total":      len(services),
		"healthy":    healthy,
		"errored":    errored,
		"services":   summary,
	}, nil))
}

func handleServiceClear(w http.ResponseWriter, r *http.Request) {
	count := registry.Size()
	registry.Clear()
	healthMonitor.Stop()
	healthMonitor.history = map[string][]HistoryEntry{}
	sendJSON(w, 200, ok(map[string]interface{}{"cleared": true, "count": count}, nil))
}

func cloneMap(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
