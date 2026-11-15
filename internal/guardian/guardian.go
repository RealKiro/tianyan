// Package guardian implements a process watchdog that restarts the student
// client if it is intentionally terminated, optionally rebooting the system
// after repeated exits.
package guardian

import (
	"log"
	"os"
	"os/exec"
	"time"
)

// Options configures the watchdog.
type Options struct {
	PID         int
	Exe         string
	Args        []string
	RebootAfter int
	StopPath    string
	Interval    time.Duration
}

// Run monitors PID and restarts Exe when it exits, until StopPath exists.
func Run(o Options) {
	if o.Interval <= 0 {
		o.Interval = 2 * time.Second
	}
	consecutive := 0
	for {
		time.Sleep(o.Interval)
		if o.StopPath != "" {
			if _, err := os.Stat(o.StopPath); err == nil {
				log.Printf("guardian: stop file present, exiting")
				return
			}
		}
		if processAlive(o.PID) {
			consecutive = 0
			continue
		}
		log.Printf("guardian: student pid %d exited", o.PID)
		consecutive++
		if o.RebootAfter > 0 && consecutive >= o.RebootAfter {
			log.Printf("guardian: %d consecutive exits, rebooting system", consecutive)
			rebootSystem()
			return
		}
		pid, err := start(o.Exe, o.Args)
		if err != nil {
			log.Printf("guardian: restart failed: %v", err)
			continue
		}
		o.PID = pid
		log.Printf("guardian: restarted student pid %d", pid)
	}
}

func start(exe string, args []string) (int, error) {
	cmd := exec.Command(exe, args...)
	hideConsole(cmd)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}
