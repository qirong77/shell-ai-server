package main

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// CheckResult holds the sub-checks performed for a single service health probe.
type CheckResult struct {
	PIDAlive bool             `json:"pidAlive"`
	PortOpen bool             `json:"portOpen"`
	HTTPOk   *HTTPCheckResult `json:"httpOk"`
}

type HTTPCheckResult struct {
	Status int  `json:"status"`
	OK     bool `json:"ok"`
}

// HistoryEntry is one health probe recorded for a service.
type HistoryEntry struct {
	Time    string      `json:"time"`
	Healthy bool        `json:"healthy"`
	Checks  CheckResult `json:"checks"`
}

const maxHistory = 20

// HealthMonitor periodically checks registered services and auto-updates their
// status. Each service gets its own goroutine driven by a ticker whose interval
// respects the per-service healthCheck.interval (min 10s, default 30s).
type HealthMonitor struct {
	registry *ServiceRegistry
	taskMgr  *TaskManager

	mu       sync.Mutex
	cancel   map[string]context.CancelFunc
	history  map[string][]HistoryEntry
	checking map[string]bool
	started  bool
}

func NewHealthMonitor(registry *ServiceRegistry, taskMgr *TaskManager) *HealthMonitor {
	return &HealthMonitor{
		registry: registry,
		taskMgr:  taskMgr,
		cancel:   map[string]context.CancelFunc{},
		history:  map[string][]HistoryEntry{},
		checking: map[string]bool{},
	}
}

func (hm *HealthMonitor) Start() {
	hm.mu.Lock()
	if hm.started {
		hm.mu.Unlock()
		return
	}
	hm.started = true
	hm.mu.Unlock()
	for _, s := range hm.registry.Query(nil) {
		hm.reschedule(s.ID)
	}
}

func (hm *HealthMonitor) Stop() {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	for id, cancel := range hm.cancel {
		cancel()
		delete(hm.cancel, id)
	}
	hm.started = false
}

func (hm *HealthMonitor) intervalOf(s *ServiceRecord) time.Duration {
	sec := 30
	if s.HealthCheck != nil && s.HealthCheck.Interval > 0 {
		sec = s.HealthCheck.Interval
	}
	if sec < 10 {
		sec = 10
	}
	return time.Duration(sec) * time.Second
}

func (hm *HealthMonitor) reschedule(id string) {
	hm.mu.Lock()
	record := hm.registry.Get(id)
	if record == nil {
		hm.mu.Unlock()
		return
	}
	if cancel, ok := hm.cancel[id]; ok {
		cancel()
		delete(hm.cancel, id)
	}
	ctx, cancel := context.WithCancel(context.Background())
	hm.cancel[id] = cancel
	interval := hm.intervalOf(record)
	hm.mu.Unlock()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				hm.Check(id)
			}
		}
	}()
}

func (hm *HealthMonitor) Check(id string) {
	hm.mu.Lock()
	if hm.checking[id] {
		hm.mu.Unlock()
		return
	}
	hm.checking[id] = true
	hm.mu.Unlock()

	defer func() {
		hm.mu.Lock()
		delete(hm.checking, id)
		hm.mu.Unlock()
	}()

	record := hm.registry.Get(id)
	if record == nil {
		hm.mu.Lock()
		if cancel, ok := hm.cancel[id]; ok {
			cancel()
			delete(hm.cancel, id)
		}
		delete(hm.history, id)
		hm.mu.Unlock()
		return
	}

	checks := CheckResult{}
	if record.PID != nil {
		checks.PIDAlive = pidAlive(*record.PID)
	}
	if record.Port != nil {
		checks.PortOpen = checkPort(hostOf(record), *record.Port)
	}
	if record.HealthCheck != nil && record.HealthCheck.URL != nil && *record.HealthCheck.URL != "" {
		checks.HTTPOk = checkHTTP(*record.HealthCheck.URL)
	}

	healthy := checks.PIDAlive || checks.PortOpen || (checks.HTTPOk != nil && checks.HTTPOk.OK)
	newStatus := "error"
	if healthy {
		newStatus = "running"
	}

	// Record history first, then update status if it changed.
	hm.mu.Lock()
	hist := hm.history[id]
	if hist == nil {
		hist = []HistoryEntry{}
	}
	hist = append(hist, HistoryEntry{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Healthy: healthy,
		Checks:  checks,
	})
	if len(hist) > maxHistory {
		hist = hist[len(hist)-maxHistory:]
	}
	hm.history[id] = hist
	hm.mu.Unlock()

	if record.Status != newStatus {
		metadata := map[string]interface{}{}
		if record.Metadata != nil {
			for k, v := range record.Metadata {
				metadata[k] = v
			}
		}
		metadata["lastHealthCheck"] = time.Now().UTC().Format(time.RFC3339)
		metadata["lastHealthStatus"] = newStatus
		hm.registry.Update(id, map[string]interface{}{"status": newStatus, "metadata": metadata})
	}
}

func (hm *HealthMonitor) OnServiceChanged(id string) {
	if !hm.started {
		return
	}
	hm.reschedule(id)
	hm.Check(id)
}

func (hm *HealthMonitor) GetHistory(id string) []HistoryEntry {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	h := hm.history[id]
	if h == nil {
		return []HistoryEntry{}
	}
	out := make([]HistoryEntry, len(h))
	copy(out, h)
	return out
}

func (hm *HealthMonitor) IsMonitoring(id string) bool {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	_, ok := hm.cancel[id]
	return ok
}

func (hm *HealthMonitor) Remove(id string) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	if cancel, ok := hm.cancel[id]; ok {
		cancel()
		delete(hm.cancel, id)
	}
	delete(hm.history, id)
}

func hostOf(s *ServiceRecord) string {
	if s.Host != "" {
		return s.Host
	}
	return "localhost"
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// kill(pid, 0) style: send signal 0 to probe existence.
	err := signalProbe(pid)
	return err == nil
}

func checkPort(host string, port int) bool {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func checkHTTP(url string) *HTTPCheckResult {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return &HTTPCheckResult{OK: false}
	}
	defer resp.Body.Close()
	return &HTTPCheckResult{Status: resp.StatusCode, OK: resp.StatusCode >= 200 && resp.StatusCode < 400}
}
