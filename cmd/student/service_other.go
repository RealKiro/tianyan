//go:build !windows

package main

import (
	"errors"

	"tianyan/internal/student"
)

func serviceInstall() error   { return errors.New("服务模式仅支持 Windows") }
func serviceUninstall() error { return errors.New("服务模式仅支持 Windows") }
func runService(cfg student.Config) error {
	return errors.New("服务模式仅支持 Windows")
}
