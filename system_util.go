package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// getServerCwd returns the process working directory at startup.
func getServerCwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

// currentEnv returns os.Environ() as a slice for passing to child processes.
func currentEnv() []string {
	return os.Environ()
}

// readMeminfoField reads a value (in bytes) from /proc/meminfo on Linux.
func readMeminfoField(field string) (int64, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, field+":") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				kb, err := strconv.ParseInt(parts[1], 10, 64)
				if err == nil {
					return kb * 1024, nil
				}
			}
		}
	}
	return 0, os.ErrNotExist
}

// atofSafe parses a float64, returning 0 on failure.
func atofSafe(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}

// portEntry is a single listening port with its associated PID (if known).
type portEntry struct {
	Port int    `json:"port"`
	PID  *int   `json:"pid"`
	Raw  string `json:"raw"`
}

// listListeningPorts returns the set of listening TCP ports. It tries `ss`
// first (Linux), then `lsof`, and falls back to parsing /proc/net/tcp.
func listListeningPorts() []map[string]interface{} {
	if ports, ok := portsFromSS(); ok && len(ports) > 0 {
		return ports
	}
	if ports, ok := portsFromLsof(); ok && len(ports) > 0 {
		return ports
	}
	return portsFromProc()
}

func portsFromSS() ([]map[string]interface{}, bool) {
	out, err := exec.Command("/bin/sh", "-c", "ss -tlnp 2>/dev/null").Output()
	if err != nil {
		return nil, false
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	seen := map[int]bool{}
	ports := []map[string]interface{}{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Example: LISTEN 0 128 0.0.0.0:9100 ... users:(("proc",pid=123,fd=5))
		if !strings.Contains(line, "LISTEN") {
			continue
		}
		port := extractPort(line)
		if port <= 0 || seen[port] {
			continue
		}
		seen[port] = true
		pid := extractPIDFromSS(line)
		ports = append(ports, map[string]interface{}{"port": port, "pid": pid, "raw": line})
	}
	sortByPort(ports)
	return ports, len(ports) > 0
}

func portsFromLsof() ([]map[string]interface{}, bool) {
	out, err := exec.Command("/bin/sh", "-c", "lsof -i -P -n 2>/dev/null").Output()
	if err != nil {
		return nil, false
	}
	seen := map[int]bool{}
	ports := []map[string]interface{}{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if !strings.Contains(line, "LISTEN") {
			continue
		}
		port := extractPort(line)
		if port <= 0 || seen[port] {
			continue
		}
		seen[port] = true
		fields := strings.Fields(line)
		var pid *int
		if len(fields) >= 2 {
			p := toInt(fields[1])
			if p > 0 {
				pid = &p
			}
		}
		ports = append(ports, map[string]interface{}{"port": port, "pid": pid, "raw": line})
	}
	sortByPort(ports)
	return ports, len(ports) > 0
}

func portsFromProc() []map[string]interface{} {
	seen := map[int]bool{}
	ports := []map[string]interface{}{}
	for _, file := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 4 {
				continue
			}
			if fields[3] != "0A" { // LISTEN state
				continue
			}
			port := parseHexPort(fields[1])
			if port <= 0 || seen[port] {
				continue
			}
			seen[port] = true
			ports = append(ports, map[string]interface{}{"port": port, "pid": nil, "raw": line})
		}
	}
	sortByPort(ports)
	return ports
}

func extractPort(line string) int {
	// Prefer the IPv4/IPv6 local-address token (e.g. "0.0.0.0:9100",
	// "[::]:443", or "*:9100"). Walk the fields and take the first one whose
	// trailing port part is a pure numeric token. This avoids the "users:"
	// segment (which contains a colon and, when multiple procs are listed,
	// can leak a PID digits as the port).
	for _, f := range strings.Fields(line) {
		idx := strings.LastIndex(f, ":")
		if idx < 0 {
			continue
		}
		tail := f[idx+1:]
		if tail == "" || tail[0] < '0' || tail[0] > '9' {
			continue
		}
		n, err := strconv.Atoi(strings.TrimRight(tail, ".,;/"))
		if err == nil && n > 0 && n <= 65535 {
			return n
		}
	}
	return 0
}

func parseHexPort(addrCol string) int {
	parts := strings.Split(addrCol, ":")
	if len(parts) < 2 {
		return 0
	}
	hexStr := parts[1]
	n, err := strconv.ParseUint(hexStr, 16, 32)
	if err != nil {
		return 0
	}
	return int(n)
}

func extractPIDFromSS(line string) *int {
	// users:(("proc",pid=123,fd=5))
	idx := strings.Index(line, "pid=")
	if idx < 0 {
		return nil
	}
	rest := line[idx+4:]
	digits := ""
	for _, c := range rest {
		if c >= '0' && c <= '9' {
			digits += string(c)
		} else if digits != "" {
			break
		}
	}
	if digits == "" {
		return nil
	}
	p, err := strconv.Atoi(digits)
	if err != nil || p <= 0 {
		return nil
	}
	return &p
}

func sortByPort(ports []map[string]interface{}) {
	for i := 0; i < len(ports); i++ {
		for j := i + 1; j < len(ports); j++ {
			pi, _ := ports[i]["port"].(int)
			pj, _ := ports[j]["port"].(int)
			if pj < pi {
				ports[i], ports[j] = ports[j], ports[i]
			}
		}
	}
}
