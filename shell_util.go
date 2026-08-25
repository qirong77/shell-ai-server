package main

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// serverCwd is the working directory the server runs in. It is captured once at
// startup and used as the default cwd for shell and file operations.
var serverCwd = getServerCwd()

// limitedBuffer is a bytes.Buffer that truncates writes beyond a max size so a
// runaway command can't exhaust memory (mirrors Node's maxBuffer).
type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.max <= 0 {
		return b.buf.Write(p)
	}
	remaining := b.max - b.buf.Len()
	if remaining <= 0 {
		return len(p), nil // silently drop
	}
	if len(p) > remaining {
		n, err := b.buf.Write(p[:remaining])
		return n, err
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) String() string {
	return b.buf.String()
}

const maxCmdBuffer = 5 * 1024 * 1024

// prepareCommand builds an /bin/sh -c process for a command string.
func prepareCommand(command, cwd string, env map[string]interface{}) *exec.Cmd {
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = cwd
	setCmdEnv(cmd, map[string]interface{}{"env": env})
	prepareProcess(cmd)
	return cmd
}

// runScriptResult is the output of a multi-line script execution.
type runScriptResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	TimedOut bool
	ErrText  string
}

// execScript runs a multi-line script via the given shell, writing the script to
// the shell's stdin (mirrors Node's spawn + stdin.write(script)).
func execScript(script, shell, cwd string, timeoutMS int, env interface{}) runScriptResult {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()

	cmd := exec.Command(shell, scriptArgs(shell, script)...)
	cmd.Dir = cwd
	if envMap, ok := env.(map[string]interface{}); ok {
		setCmdEnv(cmd, map[string]interface{}{"env": envMap})
	} else {
		cmd.Env = currentEnv()
	}
	prepareProcess(cmd)

	var stdoutBuf, stderrBuf limitedBuffer
	stdoutBuf.max = maxCmdBuffer
	stderrBuf.max = maxCmdBuffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	// The script is written to stdin rather than passed as an arg so that
	// arbitrary multi-line content is handled by the shell itself.
	cmd.Stdin = strings.NewReader(script)

	err := cmd.Run()
	res := runScriptResult{ExitCode: -1, Stdout: stdoutBuf.String(), Stderr: stderrBuf.String()}
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			res.TimedOut = true
			res.ExitCode = -1
		} else if exErr, ok := err.(interface{ ExitCode() int }); ok {
			res.ExitCode = exErr.ExitCode()
			res.ErrText = err.Error()
		} else {
			res.ExitCode = -1
			res.ErrText = err.Error()
		}
	} else {
		res.ExitCode = 0
	}
	return res
}

func scriptArgs(shell, script string) []string {
	// bash/sh: use -c so we can pass the whole script as one argument, which is
	// the most robust cross-platform approach for multi-line scripts.
	if strings.Contains(shell, "bash") || strings.HasSuffix(shell, "/sh") || shell == "sh" {
		return []string{"-c", script}
	}
	// Windows cmd.exe style (not on target server, but harmless).
	return []string{"/c", script}
}

// execAsync is a helper used by the /batch endpoint to run a command and get a
// shell result.
func execAsync(command, cwd string, env map[string]interface{}) ShellExecResult {
	return execCommandCtx(command, cwd, 30000, env)
}
