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
	"os/signal"
	"strings"
	"time"

	"tianyan/internal/block"
	"tianyan/internal/config"
	"tianyan/internal/guardian"
	"tianyan/internal/password"
	"tianyan/internal/student"
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

	// Normal student mode.
	cfg := student.DefaultConfig()
	flag.StringVar(&cfg.Name, "name", cfg.Name, "学生机显示名称")
	flag.StringVar(&cfg.StudentID, "id", "", "学生编号")
	flag.StringVar(&cfg.Group, "group", "", "分组名称")
	flag.StringVar(&cfg.TeacherIP, "teacher", "", "教师机 IP（留空则自动发现）")
	flag.IntVar(&cfg.ServerPort, "port", cfg.ServerPort, "教师机控制 TCP 端口")
	flag.StringVar(&cfg.DataDir, "data", cfg.DataDir, "数据/接收文件目录")
	flag.BoolVar(&cfg.AutoMute, "auto-mute", cfg.AutoMute, "教师机上线后自动静音")
	flag.BoolVar(&cfg.RebootOnOffline, "reboot-on-offline", false, "检测到断网后倒计时重启（默认关闭）")
	flag.DurationVar(&cfg.OfflineTimeout, "offline-timeout", cfg.OfflineTimeout, "断网判定超时")
	flag.Parse()

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
		_ = os.Remove(config.Path())
		_ = os.Remove(config.StopPath())
		log.Println("卸载完成：已停止守护、恢复 hosts 并删除配置")
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

	go c.Run()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	log.Println("student: shutting down")
	c.Close()
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
