//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows/svc"

	"tianyan/internal/service"
	"tianyan/internal/student"
)

func serviceInstall() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return service.Install(exe)
}

func serviceUninstall() error { return service.Uninstall() }

type studentSvc struct {
	stop func()
}

func (s *studentSvc) Execute(_ []string, r <-chan svc.ChangeRequest, ch chan<- svc.Status) (bool, uint32) {
	ch <- svc.Status{State: svc.StartPending}
	ch <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for c := range r {
		switch c.Cmd {
		case svc.Interrogate:
			ch <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			ch <- svc.Status{State: svc.StopPending}
			s.stop()
			return false, 0
		default:
			ch <- c.CurrentStatus
		}
	}
	return false, 0
}

func runService(cfg student.Config) error {
	c, err := student.New(cfg)
	if err != nil {
		return err
	}
	go c.Run()
	return svc.Run(service.Name, &studentSvc{stop: c.Close})
}
