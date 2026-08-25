package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Task is a long-running background process started via /shell/spawn.
type Task struct {
	ID        string   `json:"id"`
	PID       int      `json:"pid"`
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	Status    string   `json:"status"`
	StartedAt string   `json:"startedAt"`
	EndedAt   string   `json:"endedAt,omitempty"`
	ExitCode  int      `json:"exitCode"`
	Signal    string   `json:"signal,omitempty"`
	ErrText   string   `json:"error,omitempty"`
	LogPath   string   `json:"logPath"`
	Cwd       string   `json:"cwd"`
}

// taskEntry holds the running process alongside its persisted metadata.
type taskEntry struct {
	task    *Task
	cmd     *exec.Cmd
	logFile *os.File
}

// TaskManager tracks long-running processes started via /shell/spawn.
type TaskManager struct {
	mu     sync.RWMutex
	tasks  map[string]*taskEntry
	logDir string
}

func NewTaskManager(logDir string) *TaskManager {
	os.MkdirAll(logDir, 0755)
	return &TaskManager{
		tasks:  map[string]*taskEntry{},
		logDir: logDir,
	}
}

// Start launches a command as a background task and returns the task record.
func (tm *TaskManager) Start(command string, args []string, opts map[string]interface{}) *Task {
	id := randomUUID()
	logPath := filepath.Join(tm.logDir, "task-"+id+".log")

	cwd := "."
	if c, ok := opts["cwd"].(string); ok && c != "" {
		cwd = c
	}

	// In shell mode, run the full command line through /bin/sh -c so that
	// metacharacters and quoting behave as on a real shell.
	var cmd *exec.Cmd
	shell := true
	if s, ok := opts["shell"].(bool); ok {
		shell = s
	}
	if shell {
		full := command
		if len(args) > 0 {
			full += " " + joinArgs(args)
		}
		cmd = exec.Command("/bin/sh", "-c", full)
	} else {
		cmd = exec.Command(command, args...)
	}
	cmd.Dir = cwd
	setCmdEnv(cmd, opts)
	prepareProcess(cmd)

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		logFile = nil
	}

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	display := command
	if len(args) > 0 {
		display += " " + joinArgs(args)
	}

	task := &Task{
		ID:        id,
		Command:   display,
		Args:      args,
		Status:    "running",
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		ExitCode:  -1,
		LogPath:   logPath,
		Cwd:       cwd,
	}

	entry := &taskEntry{task: task, cmd: cmd, logFile: logFile}

	if err := cmd.Start(); err != nil {
		task.Status = "error"
		task.EndedAt = time.Now().UTC().Format(time.RFC3339)
		task.ErrText = err.Error()
		if logFile != nil {
			logFile.WriteString("\n[ERROR] " + err.Error() + "\n")
		}
	}
	if cmd.Process != nil {
		task.PID = cmd.Process.Pid
	}

	if logFile != nil {
		// Use a single writer so stdout/stderr interleave into one log file.
		var w io.Writer = logFile
		go io.Copy(io.MultiWriter(w), stdout)
		go io.Copy(io.MultiWriter(w), stderr)
	}

	tm.mu.Lock()
	tm.tasks[id] = entry
	tm.mu.Unlock()

	if cmd.Process != nil {
		go func() {
			err := cmd.Wait()
			state := cmd.ProcessState
			tm.mu.Lock()
			if state != nil {
				task.ExitCode = state.ExitCode()
			}
			if err != nil {
				if exErr, ok := err.(*exec.ExitError); ok {
					if ws, ok := exErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
						// Terminated by a signal (e.g. via /kill) -> "killed",
						// matching the Node implementation's semantics.
						task.Signal = ws.Signal().String()
						if task.Status == "running" {
							task.Status = "killed"
						}
					} else {
						// Clean exit with non-zero code, or non-signal failure.
						task.Status = "error"
					}
				} else {
					task.Status = "error"
					if task.ErrText == "" {
						task.ErrText = err.Error()
					}
				}
			} else {
				task.Status = "completed"
			}
			task.EndedAt = time.Now().UTC().Format(time.RFC3339)
			if logFile != nil {
				logFile.Close()
			}
			tm.mu.Unlock()
		}()
	}

	return task
}

func (tm *TaskManager) Get(id string) *Task {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	if e, ok := tm.tasks[id]; ok {
		copy := *e.task
		return &copy
	}
	return nil
}

func (tm *TaskManager) List() []*Task {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	out := make([]*Task, 0, len(tm.tasks))
	for _, e := range tm.tasks {
		copy := *e.task
		out = append(out, &copy)
	}
	return out
}

func (tm *TaskManager) Kill(id, signal string) bool {
	tm.mu.RLock()
	entry, ok := tm.tasks[id]
	tm.mu.RUnlock()
	if !ok || entry.task.Status != "running" {
		return false
	}
	return killProcess(entry.cmd, signal)
}

func (tm *TaskManager) GetLog(id string, lines int) string {
	tm.mu.RLock()
	entry, ok := tm.tasks[id]
	tm.mu.RUnlock()
	if !ok {
		return ""
	}
	if lines < 1 {
		lines = 100
	}
	if lines > 5000 {
		lines = 5000
	}
	data, err := os.ReadFile(entry.task.LogPath)
	if err != nil {
		return ""
	}
	return tailLines(string(data), lines)
}

func (tm *TaskManager) Delete(id string) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if e, ok := tm.tasks[id]; ok && e.task.Status != "running" {
		delete(tm.tasks, id)
		return true
	}
	return false
}

func (tm *TaskManager) Cleanup(maxAge time.Duration) {
	now := time.Now()
	tm.mu.Lock()
	defer tm.mu.Unlock()
	for id, e := range tm.tasks {
		if e.task.Status == "running" || e.task.EndedAt == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, e.task.EndedAt); err == nil {
			if now.Sub(t) > maxAge {
				delete(tm.tasks, id)
			}
		}
	}
}

func (tm *TaskManager) Shutdown() {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	for _, e := range tm.tasks {
		if e.task.Status == "running" && e.cmd.Process != nil {
			killProcess(e.cmd, "SIGTERM")
		}
		if e.logFile != nil {
			e.logFile.Close()
		}
	}
}

func joinArgs(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		if strings.ContainsAny(a, " \t\"'") {
			out += `"` + a + `"`
		} else {
			out += a
		}
	}
	return out
}

func tailLines(s string, n int) string {
	if n <= 0 {
		return ""
	}
	var lines []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
			if len(lines) > n {
				lines = lines[len(lines)-n:]
			}
		}
	}
	return strings.Join(lines, "\n")
}
