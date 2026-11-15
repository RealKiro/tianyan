// Command teacher runs the 天眼 (TianYan) teacher server and web console.
// On Windows it also shows the always-on-top quick-action toolbar docked to
// the top edge of the screen (极域 style).
package main

import (
	"flag"
	"log"
	"time"

	"tianyan/internal/dock"
	"tianyan/internal/teacher"
)

// Docked-bar button ids.
const (
	btnConsole = 1
	btnDemo    = 2
	btnBlack   = 3
	btnLock    = 4
	btnMute    = 5
	btnPower   = 7
	btnReboot  = 8
)

func main() {
	cfg := teacher.DefaultConfig()
	noToolbar := flag.Bool("no-toolbar", false, "隐藏屏幕顶部的教师快捷工具栏")
	flag.StringVar(&cfg.Name, "name", cfg.Name, "教师机名称")
	flag.IntVar(&cfg.ControlPort, "port", cfg.ControlPort, "学生控制 TCP 端口")
	flag.StringVar(&cfg.HTTPAddr, "http", cfg.HTTPAddr, "控制台 HTTP 监听地址")
	flag.StringVar(&cfg.DataDir, "data", cfg.DataDir, "数据/接收作业目录")
	flag.StringVar(&cfg.AuthKey, "key", "", "共享认证密钥（留空则不认证）")
	flag.IntVar(&cfg.DemoFPS, "demo-fps", cfg.DemoFPS, "屏幕广播帧率（教师屏幕广播；百兆局域网建议 5）")
	flag.IntVar(&cfg.DemoWidth, "demo-width", cfg.DemoWidth, "屏幕广播帧最大宽度，0=原始分辨率（百兆局域网建议 1280）")
	flag.IntVar(&cfg.DemoQuality, "demo-quality", cfg.DemoQuality, "屏幕广播 JPEG 质量 1..100")
	flag.Parse()

	s, err := teacher.New(cfg)
	if err != nil {
		log.Fatalf("teacher: %v", err)
	}

	startTeacherBar(s, *noToolbar)

	log.Printf("天眼教师端启动（控制台 http://localhost%s）", cfg.HTTPAddr)
	log.Fatal(s.Run())
}

// startTeacherBar shows the quick-action toolbar docked to the top edge.
func startTeacherBar(s *teacher.Server, disabled bool) {
	if disabled || !dock.Supported() {
		return
	}
	bar, err := dock.NewBar(dock.Options{
		Title:   "天眼教师端",
		Teacher: true,
		Buttons: []dock.Button{
			{ID: btnConsole, Label: "控制台"},
			{ID: btnDemo, Label: "广播"},
			{ID: btnBlack, Label: "黑屏"},
			{ID: btnLock, Label: "锁定"},
			{ID: btnMute, Label: "静音"},
			{ID: btnPower, Label: "关机"},
			{ID: btnReboot, Label: "重启"},
		},
		OnClick: func(id int) {
			switch id {
			case btnConsole:
				s.QuickCommand("console")
			case btnDemo:
				s.QuickCommand("broadcast")
			case btnBlack:
				s.QuickCommand("black")
			case btnLock:
				s.QuickCommand("lock")
			case btnMute:
				s.QuickCommand("mute")
			case btnPower:
				if dock.MessageBox("天眼 · 远程关机", "确认对所有在线学生执行【关机】？") {
					s.QuickCommand("shutdown")
				}
			case btnReboot:
				if dock.MessageBox("天眼 · 远程重启", "确认对所有在线学生执行【重启】？") {
					s.QuickCommand("reboot")
				}
			}
		},
	})
	if err != nil {
		log.Printf("teacher: docked toolbar: %v", err)
		return
	}
	bar.SetLabel(s.QuickStatus())
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for range t.C {
			bar.SetLabel(s.QuickStatus())
		}
	}()
}
