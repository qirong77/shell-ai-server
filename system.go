package main

import (
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
)

func handleSystem(w http.ResponseWriter, r *http.Request) {
	sendJSON(w, 200, ok(buildSystemInfo(), nil))
}

func buildSystemInfo() map[string]interface{} {
	host, _ := os.Hostname()
	homedir, _ := os.UserHomeDir()
	info := map[string]interface{}{
		"hostname":    host,
		"platform":    runtime.GOOS,
		"arch":        runtime.GOARCH,
		"cpuCount":    runtime.NumCPU(),
		"cpus":        readCPUInfo(),
		"totalMemory": totalSystemMemory(),
		"freeMemory":  freeSystemMemory(),
		"uptime":      systemUptime(),
		"loadavg":     systemLoadavg(),
		"network":     networkInterfaces(),
		"homedir":     homedir,
		"tmpdir":      os.TempDir(),
		"pid":         os.Getpid(),
		"cwd":         serverCwd,
		"env": map[string]interface{}{
			"PATH":  os.Getenv("PATH"),
			"SHELL": os.Getenv("SHELL"),
			"HOME":  os.Getenv("HOME"),
			"USER":  os.Getenv("USER"),
		},
	}
	return info
}

func handleSystemPorts(w http.ResponseWriter, r *http.Request) {
	ports := listListeningPorts()
	sendJSON(w, 200, ok(ports, map[string]interface{}{"count": len(ports)}))
}

func handleSystemDisk(w http.ResponseWriter, r *http.Request) {
	raw, err := diskOutput()
	if err != nil {
		sendJSON(w, 500, fail("DISK_ERROR", "Disk query failed: "+err.Error(), nil, nil))
		return
	}
	sendJSON(w, 200, ok(map[string]interface{}{"raw": raw, "parsed": parseDiskUsage(raw)}, nil))
}

func handleProcesses(w http.ResponseWriter, r *http.Request) {
	filter := queryStr(r, "filter")
	processes, err := listProcesses(filter)
	if err != nil {
		sendJSON(w, 500, fail("PROC_ERROR", "Failed to list processes: "+err.Error(), nil, nil))
		return
	}
	sendJSON(w, 200, ok(processes, map[string]interface{}{"count": len(processes)}))
}

func handleProcessKill(w http.ResponseWriter, r *http.Request, params map[string]string) {
	pid := toInt(params["pid"])
	if pid <= 0 {
		sendJSON(w, 400, fail("INVALID_PARAM", "Invalid PID", nil, nil))
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
	if err := signalPID(pid, signal); err != nil {
		sendJSON(w, 500, fail("PROC_ERROR", "Failed to kill process "+itoa(pid)+": "+err.Error(), nil, nil))
		return
	}
	sendJSON(w, 200, ok(map[string]interface{}{"pid": pid, "signal": signal, "killed": true}, nil))
}

func handleEnvGet(w http.ResponseWriter, r *http.Request) {
	key := queryStr(r, "key")
	if key != "" {
		sendJSON(w, 200, ok(map[string]interface{}{"key": key, "value": os.Getenv(key)}, nil))
		return
	}
	env := map[string]interface{}{}
	for _, kv := range os.Environ() {
		parts := strings.SplitN(kv, "=", 2)
		env[parts[0]] = parts[1]
	}
	sendJSON(w, 200, ok(env, nil))
}

func handleEnvSet(w http.ResponseWriter, r *http.Request) {
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
	key := stringField(obj, "key")
	if key == "" {
		sendJSON(w, 400, fail("MISSING_PARAM", "Missing 'key' field", nil, nil))
		return
	}
	value := stringField(obj, "value")
	os.Setenv(key, value)
	sendJSON(w, 200, ok(map[string]interface{}{"key": key, "value": value, "set": true}, nil))
}

// ---------------------------------------------------------------------------
// system helpers

func readCPUInfo() []map[string]interface{} {
	out := []map[string]interface{}{}
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		out = append(out, map[string]interface{}{"model": runtime.GOARCH, "speed": 0})
		return out
	}
	var current map[string]interface{}
	flush := func() {
		if current != nil {
			out = append(out, current)
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			flush()
			current = nil
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		if current == nil {
			current = map[string]interface{}{}
		}
		if key == "model name" {
			current["model"] = val
		} else if key == "cpu MHz" {
			current["speed"] = atofSafe(val)
		}
	}
	flush()
	return out
}

func totalSystemMemory() int64 {
	if v, err := readMeminfoField("MemTotal"); err == nil {
		return v
	}
	return 0
}

func freeSystemMemory() int64 {
	if v, err := readMeminfoField("MemAvailable"); err == nil {
		return v
	}
	return 0
}

func systemUptime() int64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) > 0 {
		return int64(atofSafe(fields[0]))
	}
	return 0
}

func systemLoadavg() []float64 {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return []float64{}
	}
	fields := strings.Fields(string(data))
	loads := make([]float64, 0, 3)
	for i := 0; i < 3 && i < len(fields); i++ {
		loads = append(loads, atofSafe(fields[i]))
	}
	return loads
}

func networkInterfaces() map[string]interface{} {
	out := map[string]interface{}{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		list := []map[string]interface{}{}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ipnet.IP.IsLoopback() {
				continue
			}
			list = append(list, map[string]interface{}{
				"address": ipnet.IP.String(),
				"family":  ipFamily(ipnet.IP),
				"mac":     iface.HardwareAddr.String(),
			})
		}
		if len(list) > 0 {
			out[iface.Name] = list
		}
	}
	return out
}

func ipFamily(ip net.IP) string {
	if ip.To4() != nil {
		return "IPv4"
	}
	return "IPv6"
}
