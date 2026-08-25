package main

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// signalMap maps signal names (as accepted by the API) to syscall signals.
// The values are Unix signals; on Windows these are ignored gracefully.
var signalMap = map[string]syscall.Signal{
	"SIGHUP":  syscall.SIGHUP,
	"SIGINT":  syscall.SIGINT,
	"SIGQUIT": syscall.SIGQUIT,
	"SIGKILL": syscall.SIGKILL,
	"SIGTERM": syscall.SIGTERM,
	"SIGUSR1": syscall.SIGUSR1,
	"SIGUSR2": syscall.SIGUSR2,
	"SIGCONT": syscall.SIGCONT,
	"SIGSTOP": syscall.SIGSTOP,
}

// normalizeSignal uppercases and normalizes a user-supplied signal name.
func normalizeSignal(s string) string {
	if s == "" {
		return "SIGTERM"
	}
	s = strings.ToUpper(s)
	if !strings.HasPrefix(s, "SIG") {
		s = "SIG" + s
	}
	return s
}

// prepareProcess sets process-group attributes so that killProcess can signal
// the whole group via a negative PID. On Unix platforms Setpgid is available.
func prepareProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcess sends a signal to the process group backing cmd.
func killProcess(cmd *exec.Cmd, signalName string) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	sig := signalMap[normalizeSignal(signalName)]
	// Use -pid so the whole process group is signalled (mimics killing children).
	err := syscall.Kill(-cmd.Process.Pid, sig)
	if err != nil {
		// Fall back to signalling just the process.
		err = cmd.Process.Signal(sig)
	}
	return err == nil
}

// setCmdEnv applies any extra environment variables from opts to the command.
func setCmdEnv(cmd *exec.Cmd, opts map[string]interface{}) {
	env := os.Environ()
	if extras, ok := opts["env"].(map[string]interface{}); ok {
		for k, v := range extras {
			env = append(env, k+"="+anyString(v))
		}
	}
	cmd.Env = env
}

// signalProbe checks whether a process exists by sending signal 0, which is the
// standard "poll process existence" mechanism (like process.kill(pid, 0)).
func signalProbe(pid int) error {
	return syscall.Kill(pid, 0)
}
