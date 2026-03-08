package keepalive

import (
	"log"
	"os/exec"
	"runtime"
)

// PreventSleep attempts to prevent the system from sleeping.
// It returns a function that should be called to allow the system to sleep again.
func PreventSleep() func() {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		// macOS: caffeinate -i (prevent idle sleep)
		cmd = exec.Command("caffeinate", "-i")
	case "linux":
		// Linux: systemd-inhibit
		cmd = exec.Command("systemd-inhibit", "--what=sleep", "--why=auto-translate-running", "--mode=block", "sleep", "infinity")
	}

	if cmd != nil {
		if err := cmd.Start(); err != nil {
			log.Printf("Failed to start sleep prevention: %v", err)
			return func() {}
		}
		log.Printf("System sleep prevented (OS: %s, PID: %d)", runtime.GOOS, cmd.Process.Pid)
		return func() {
			if err := cmd.Process.Kill(); err != nil {
				log.Printf("Failed to kill sleep prevention process: %v", err)
			} else {
				log.Printf("System sleep prevention released.")
			}
			cmd.Wait() // clean up zombie process
		}
	}

	log.Printf("Sleep prevention not implemented for OS: %s", runtime.GOOS)
	return func() {}
}
