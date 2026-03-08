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
	workDir := filepath.Join("..")

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

	cmd := exec.Command("go", "run", "cmd/webrunner/main.go")
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
