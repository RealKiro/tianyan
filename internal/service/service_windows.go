//go:build windows

// Package service installs the student client as a Windows service.
package service

import (
	"fmt"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Name is the Windows service name.
const Name = "TianYanStudent"

// Install registers the student client as an auto-start service.
func Install(exePath string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	if s, err := m.OpenService(Name); err == nil {
		s.Close()
		return fmt.Errorf("service %s already exists", Name)
	}

	s, err := m.CreateService(Name, exePath, mgr.Config{
		DisplayName: "天眼学生端",
		Description: "天眼电子教室学生端：屏幕监控、远程控制、进程守护与黑白名单管控",
		StartType:   mgr.StartAutomatic,
	}, "-service", "run")
	if err != nil {
		return err
	}
	s.Close()
	return nil
}

// Uninstall stops and removes the Windows service.
func Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	s, err := m.OpenService(Name)
	if err != nil {
		return fmt.Errorf("service %s not found", Name)
	}
	defer s.Close()
	_, _ = s.Control(svc.Stop)
	return s.Delete()
}
