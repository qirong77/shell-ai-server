package main

import (
	"os/exec"
	"strings"
	"syscall"
)

// listProcesses runs `ps aux` (optionally grepped by filter) and parses the
// output into structured process records.
func listProcesses(filter string) ([]map[string]interface{}, error) {
	cmd := "ps aux"
	if filter != "" {
		cmd = "ps aux | grep " + filter + " | grep -v grep"
	}
	out, err := exec.Command("/bin/sh", "-c", cmd).Output()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	processes := make([]map[string]interface{}, 0, len(lines))
	for i, line := range lines {
		if i == 0 {
			continue // header
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(processes) >= 200 {
			break
		}
		parts := strings.Fields(line)
		if len(parts) < 9 {
			continue
		}
		processes = append(processes, map[string]interface{}{
			"user":    parts[0],
			"pid":     toInt(parts[1]),
			"cpu":     parts[2],
			"mem":     parts[3],
			"vsz":     parts[4],
			"rss":     parts[5],
			"tty":     parts[6],
			"stat":    parts[7],
			"start":   parts[8],
			"command": strings.Join(parts[9:], " "),
		})
	}
	return processes, nil
}

// signalPID sends a signal (e.g. SIGTERM) to a specific process ID.
func signalPID(pid int, signalName string) error {
	sig := signalMap[normalizeSignal(signalName)]
	return syscall.Kill(pid, sig)
}
