package test

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

type TestServer struct {
	cmd     *exec.Cmd
	BaseURL string
	WorkDir string
}

func startServer(t *testing.T) *TestServer {
	return startServerAt(t, "")
}

// startServerAt launches the webrunner test server in a sandboxed working
// directory. With dir == "" a fresh sandbox is created; passing a previous
// TestServer's WorkDir reuses that sandbox so on-disk task state survives a
// server restart (the persistence/restart tests depend on this).
func startServerAt(t *testing.T, dir string) *TestServer {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve project root: %v", err)
	}
	workDir := dir
	if workDir == "" {
		workDir = t.TempDir()
	}
	// Read-only assets are symlinked into the sandbox so the server's
	// relative lookups (web/, prompts/) resolve, while temp_uploads/ (task
	// states, uploads) stays inside the sandbox and never touches the real
	// project data.
	for _, sub := range []string{"web", "prompts"} {
		link := filepath.Join(workDir, sub)
		if _, err := os.Lstat(link); err == nil {
			continue // sandbox reuse: symlink already in place
		}
		if err := os.Symlink(filepath.Join(root, sub), link); err != nil {
			t.Fatalf("symlink %s into sandbox: %v", sub, err)
		}
	}

	// Block openBrowser by injecting a dummy open/xdg-open
	binDir := t.TempDir()
	os.WriteFile(filepath.Join(binDir, "open"), []byte("#!/bin/sh\nexit 0"), 0755)
	os.WriteFile(filepath.Join(binDir, "xdg-open"), []byte("#!/bin/sh\nexit 0"), 0755)
	os.Chmod(filepath.Join(binDir, "open"), 0755)
	os.Chmod(filepath.Join(binDir, "xdg-open"), 0755)

	env := os.Environ()
	for i, e := range env {
		if len(e) >= 5 && e[:5] == "PATH=" {
			env[i] = "PATH=" + binDir + string(os.PathListSeparator) + e[5:]
			break
		}
	}

	// Build once and run the binary directly: `go run` spawns a child process
	// that Process.Kill would NOT take down, leaking orphan servers that keep
	// processing queued tasks against real local ports.
	serverBin := filepath.Join(binDir, "webrunner_test_bin")
	buildCmd := exec.Command("go", "build", "-o", serverBin, "./cmd/webrunner")
	buildCmd.Dir = root // module root; the sandbox has no go.mod
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build webrunner: %v\n%s", err, out)
	}
	cmd := exec.Command(serverBin)
	cmd.Dir = workDir
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start cmd: %v", err)
	}

	var url string
	reader := bufio.NewReader(stdout)
	re := regexp.MustCompile(`http://localhost:\d+`)

	done := make(chan struct{})
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if match := re.FindString(line); match != "" {
				url = match
				close(done)
				io.Copy(io.Discard, reader)
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("Timeout waiting for server to start")
	}

	return &TestServer{
		cmd:     cmd,
		BaseURL: url,
		WorkDir: workDir,
	}
}

func (s *TestServer) Close() {
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
		s.cmd.Wait()
	}
}
