// Command student runs the 天眼 (TianYan) student client. It also provides
// the process guardian (-guard), install-time password (-set-password) and
// password-protected uninstall (-uninstall) modes.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"time"

	"tianyan/internal/block"
	"tianyan/internal/config"
	"tianyan/internal/dock"
	"tianyan/internal/guardian"
	"tianyan/internal/nativeui"
	"tianyan/internal/password"
	"tianyan/internal/student"
	"tianyan/internal/tray"
)

func main() {
	// Guardian / install / uninstall modes.
	guard := flag.Bool("guard", false, "以守护进程模式运行（看护学生端）")
	pid := flag.Int("pid", 0, "被守护的学生进程 PID")
	daemon := flag.Bool("daemon", false, "启用进程守护（被退出后自动重启）")
	rebootAfter := flag.Int("reboot-after", 0, "守护进程检测到连续退出 N 次后重启系统（0=不重启）")
	setPassword := flag.Bool("set-password", false, "设置卸载密码")
	uninstall := flag.Bool("uninstall", false, "卸载（需要密码）")
	pass := flag.String("pass", "", "密码（配合 -set-password / -uninstall）")
	serviceCmd := flag.String("service", "", "服务模式：install | uninstall | run")
	install := flag.Bool("install", false, "安装：注册开机自动启动的计划任务（需管理员运行一次）")
	background := flag.Bool("background", false, "内部参数：由开机计划任务启动时隐藏控制台窗口")

	// Normal student mode.
	cfg := student.DefaultConfig()
	noToolbar := flag.Bool("no-toolbar", false, "隐藏屏幕顶部的灵动岛工具栏")
	noTray := flag.Bool("no-tray", false, "隐藏系统托盘常驻图标")
	flag.StringVar(&cfg.Name, "name", cfg.Name, "学生机显示名称")
	flag.StringVar(&cfg.StudentID, "id", "", "学生编号")
	flag.StringVar(&cfg.Group, "group", "", "分组名称")
	flag.StringVar(&cfg.TeacherIP, "teacher", "", "教师机 IP（留空则自动发现）")
	flag.IntVar(&cfg.ServerPort, "port", cfg.ServerPort, "教师机控制 TCP 端口")
	flag.StringVar(&cfg.DataDir, "data", cfg.DataDir, "数据/接收文件目录")
	flag.StringVar(&cfg.AuthKey, "key", "", "共享认证密钥（留空则不认证）")
	flag.IntVar(&cfg.VNCPort, "vnc-port", cfg.VNCPort, "VNC(RFB) 服务端口（0=关闭）")
	flag.BoolVar(&cfg.AutoMute, "auto-mute", cfg.AutoMute, "教师机上线后自动静音")
	flag.BoolVar(&cfg.RebootOnOffline, "reboot-on-offline", false, "检测到与教师失联后倒计时重启（默认关闭）")
	flag.DurationVar(&cfg.OfflineTimeout, "offline-timeout", cfg.OfflineTimeout, "断联判定超时")
	flag.BoolVar(&cfg.NetDownReboot, "netdown-reboot", cfg.NetDownReboot, "检测到网线断开（物理链路断）时自动锁屏并倒计时重启")
	flag.DurationVar(&cfg.NetDownTimeout, "netdown-timeout", cfg.NetDownTimeout, "网线断开判定时长")
	flag.DurationVar(&cfg.ReconnectRestart, "reconnect-restart", cfg.ReconnectRestart, "与教师端失联后持续重连失败此时长则自动重启客户端进程（0=禁用）")
	flag.Parse()

	// 计划任务以 -background 启动学生端：转入无控制台窗口的后台进程。
	if *background {
		if err := spawnHidden(backgroundArgs(os.Args[1:])); err != nil {
			log.Fatalf("background: %v", err)
		}
		return
	}

	switch *serviceCmd {
	case "install":
		if err := serviceInstall(); err != nil {
			log.Fatal(err)
		}
		log.Println("服务已安装（开机自启）")
		return
	case "uninstall":
		if err := serviceUninstall(); err != nil {
			log.Fatal(err)
		}
		log.Println("服务已卸载")
		return
	case "run":
		if err := runService(cfg); err != nil {
			log.Fatal(err)
		}
		return
	}

	// 安装：注册开机自动启动的计划任务（用户登录时以最高权限后台运行）。
	if *install {
		if err := installAutostart(); err != nil {
			log.Fatalf("install: %v（请以管理员身份运行安装命令）", err)
		}
		log.Println("安装完成：学生端将随开机自动在后台运行（计划任务 TianYanStudent）")
		return
	}

	// 守护模式：看护学生进程，退出后自动重启，必要时重启系统。
	if *guard {
		c, err := config.Load()
		if err != nil {
			log.Fatalf("guardian: 读取配置失败: %v", err)
		}
		guardian.Run(guardian.Options{
			PID:         *pid,
			Exe:         c.ExePath,
			Args:        c.Args,
			RebootAfter: c.RebootAfter,
			StopPath:    config.StopPath(),
		})
		return
	}

	// 安装时设置卸载密码。
	if *setPassword {
		p := *pass
		if p == "" {
			first := promptPassword("请设置卸载密码: ")
			second := promptPassword("请再次输入确认: ")
			if first != second {
				log.Fatal("两次输入不一致")
			}
			p = first
		}
		if len(p) < 4 {
			log.Fatal("密码至少 4 位")
		}
		st, err := password.Hash(p)
		if err != nil {
			log.Fatal(err)
		}
		c, _ := config.Load()
		c.Password = st
		if err := config.Save(c); err != nil {
			log.Fatalf("保存配置失败: %v", err)
		}
		log.Println("卸载密码已设置")
		return
	}

	// 卸载：必须输入正确密码。
	if *uninstall {
		c, err := config.Load()
		if err != nil {
			log.Fatal("未找到安装配置（可能尚未安装）")
		}
		p := *pass
		if p == "" {
			p = promptPassword("请输入卸载密码: ")
		}
		if !password.Verify(p, c.Password) {
			log.Fatal("密码错误，已取消卸载")
		}
		_ = os.WriteFile(config.StopPath(), []byte("stop"), 0644)
		time.Sleep(3 * time.Second)
		if err := block.RestoreHosts(); err != nil {
			log.Printf("恢复 hosts 失败: %v", err)
		}
		if err := uninstallAutostart(); err != nil {
			log.Printf("移除开机自启计划任务失败（可能未安装）: %v", err)
		}
		_ = os.Remove(config.Path())
		_ = os.Remove(config.StopPath())
		log.Println("卸载完成：已停止守护、移除开机自启、恢复 hosts 并删除配置")
		return
	}

	// 正常学生模式。
	c, err := student.New(cfg)
	if err != nil {
		log.Fatalf("student: %v", err)
	}

	if *daemon {
		exe, _ := os.Executable()
		sc := config.Student{
			ExePath:     exe,
			Args:        restartArgs(os.Args[1:]),
			RebootAfter: *rebootAfter,
		}
		if existing, err := config.Load(); err == nil {
			sc.Password = existing.Password
		}
		if err := config.Save(sc); err != nil {
			log.Printf("保存守护配置失败: %v", err)
		} else if err := guardian.Spawn(exe, os.Getpid()); err != nil {
			log.Printf("启动守护进程失败: %v", err)
		} else {
			log.Println("进程守护已启用")
		}
	}

	log.Printf("天眼学生端 v%s 启动（小工具栏 http://127.0.0.1:5600）", student.Version)

	bar := startStudentBar(c, *noToolbar)
	startTrayIcon(c, bar, *noTray)

	// 失联自动重建：重连持续失败时退出当前进程（守护进程拉起或自拉起）。
	c.OnRestartClient = func() {
		log.Println("student: restarting client process to rebuild the connection")
		c.Shutdown()
		if !*daemon {
			_ = spawnHidden(restartArgs(os.Args[1:]))
		}
		os.Exit(0)
	}

	go c.Run()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	log.Println("student: shutting down")
	c.Close()
}

// startStudentBar shows the Dynamic-Island-style floating toolbar at the
// top of the desktop (极域 style). It hides itself while a broadcast or
// lock screen covers the desktop, and reappears afterwards.
func startStudentBar(c *student.Client, disabled bool) *dock.Bar {
	if disabled || !dock.Supported() {
		return nil
	}
	var (
		mu       sync.Mutex
		demoing  bool
		overlay  bool
		hiddenBy bool
	)
	applyVisibility := func(bar *dock.Bar) {
		mu.Lock()
		hide := demoing || overlay
		mu.Unlock()
		if hide != hiddenBy {
			hiddenBy = hide
			if hide {
				bar.Hide()
			} else {
				bar.Show()
			}
		}
	}
	bar, err := dock.NewBar(dock.Options{
		Title: "天眼学生端",
		Buttons: []dock.Button{
			{ID: 1, Label: "举手"},
			{ID: 2, Label: "消息"},
			{ID: 3, Label: "作业"},
			{ID: 4, Label: "广播"},
		},
		OnClick: func(id int) {
			switch id {
			case 1:
				c.ToggleRaiseHand()
			case 2:
				nativeui.InputBox("发送消息给老师", "消息内容：", func(text string) {
					c.SendChatMessage(text)
				})
			case 3:
				nativeui.PickFile("选择要提交的作业文件", func(path string) {
					if path != "" {
						c.SendHomework(path)
					}
				})
			case 4:
				c.RequestBroadcast()
			}
		},
	})
	if err != nil {
		log.Printf("student: toolbar bar: %v", err)
		return nil
	}
	c.OnUpdate = func() { bar.SetLabel(c.StatusLine()) }
	c.OnDemoing = func(active bool) {
		mu.Lock()
		demoing = active
		mu.Unlock()
		applyVisibility(bar)
		bar.SetLabel(c.StatusLine())
	}
	c.OnOverlay = func(on bool) {
		mu.Lock()
		overlay = on
		mu.Unlock()
		applyVisibility(bar)
	}
	// Periodic refresh keeps connection state honest without events.
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for range t.C {
			bar.SetLabel(c.StatusLine())
		}
	}()
	bar.SetLabel(c.StatusLine())
	return bar
}

// startTrayIcon shows the resident tray icon (冰点还原 style): students
// cannot operate it; Shift+double-click asks for the admin password before
// opening the local settings page. The icon turns gray while the client is
// disconnected from the teacher, and the island toolbar buttons gray out
// with it.
func startTrayIcon(c *student.Client, bar *dock.Bar, disabled bool) {
	if disabled || !tray.Supported() {
		return
	}
	stored, _ := config.Load()
	icon, err := tray.New(tray.Options{
		Tooltip: "天眼学生端",
		Verify: func(pass string) bool {
			if stored.Password.Hash == "" {
				dock.MessageBox("天眼 · 客户端管理", "尚未设置管理密码。\n请先运行：student -set-password")
				return false
			}
			if !password.Verify(pass, stored.Password) {
				dock.MessageBox("天眼 · 客户端管理", "密码错误。")
				return false
			}
			return true
		},
		OnUnlock: func() { nativeui.StatusBox("天眼 · 客户端管理", c.AdminInfo()) },
	})
	if err != nil {
		log.Printf("student: tray icon: %v", err)
		return
	}
	c.OnConnState = func(online bool) {
		icon.SetGray(!online)
		for id := 1; id <= 4; id++ {
			bar.SetEnabled(id, online)
		}
		bar.SetLabel(c.StatusLine())
	}
}

// restartArgs removes guardian-only flags so the restart does not spawn a
// duplicate watchdog.
func restartArgs(args []string) []string {
	out := make([]string, 0, len(args))
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if a == "-daemon" || a == "-daemon=true" || a == "-daemon=false" {
			continue
		}
		if a == "-reboot-after" {
			skipNext = true
			continue
		}
		if strings.HasPrefix(a, "-reboot-after=") {
			continue
		}
		out = append(out, a)
	}
	return out
}

func promptPassword(msg string) string {
	fmt.Print(msg)
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	return strings.TrimSpace(line)
}

// autostartTaskName is the scheduled task that relaunches the client at
// every user sign-in (registered by -install).
const autostartTaskName = "TianYanStudent"

// installAutostart registers a logon scheduled task so the installed client
// auto-runs in the background after every boot/sign-in — elevated (HIGHEST)
// so hosts-based website blocking and the exemption release work, and in
// the user session so screen capture, input control and the docked toolbar
// all operate on the interactive desktop.
func installAutostart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := installArgs(os.Args[1:])
	cmdLine := `"` + exe + `"` + " " + strings.Join(args, " ") + " -background"
	return exec.Command("schtasks", "/Create", "/F", "/TN", autostartTaskName,
		"/TR", cmdLine, "/SC", "ONLOGON", "/RL", "HIGHEST").Run()
}

func uninstallAutostart() error {
	return exec.Command("schtasks", "/Delete", "/F", "/TN", autostartTaskName).Run()
}

// installArgs strips installer-only flags; everything else (name, teacher,
// daemon protection, deny rules...) carries into the autostart command.
func installArgs(args []string) []string {
	return filterFlag(args, "-install")
}

// backgroundArgs strips the internal -background flag before re-spawning.
func backgroundArgs(args []string) []string {
	return filterFlag(args, "-background")
}

func filterFlag(args []string, flagName string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a == flagName || a == flagName+"=true" || a == flagName+"=false" {
			continue
		}
		out = append(out, a)
	}
	return out
}
