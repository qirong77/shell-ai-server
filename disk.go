package main

import (
	"os/exec"
	"strings"
)

// diskOutput runs `df -h` and returns the raw stdout.
func diskOutput() (string, error) {
	out, err := exec.Command("df", "-h").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// parseDiskUsage parses `df -h` output. It handles the differing column layout
// between Linux (Filesystem Size Used Avail Use% Mounted) and macOS
// (Filesystem Size Used Avail Capacity iused ifree %iused Mounted) by locating
// the percentage column and treating the last column as the mount point.
func parseDiskUsage(output string) []map[string]interface{} {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) <= 1 {
		return []map[string]interface{}{}
	}
	out := make([]map[string]interface{}, 0, len(lines)-1)
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		if len(parts) >= 6 {
			mounted := parts[len(parts)-1]
			usePercent := ""
			for _, p := range parts {
				if strings.Contains(p, "%") {
					usePercent = p
					break
				}
			}
			out = append(out, map[string]interface{}{
				"filesystem": parts[0],
				"size":       parts[1],
				"used":       parts[2],
				"avail":      parts[3],
				"usePercent": usePercent,
				"mounted":    mounted,
			})
		} else {
			out = append(out, map[string]interface{}{
				"filesystem": parts[0],
				"raw":        line,
			})
		}
	}
	return out
}
