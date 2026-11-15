// Command student runs the 天眼 (TianYan) student client.
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"

	"tianyan/internal/student"
)

func main() {
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

	c, err := student.New(cfg)
	if err != nil {
		log.Fatalf("student: %v", err)
	}
	log.Printf("天眼学生端 v%s 启动（小工具栏 http://127.0.0.1:5600）", student.Version)

	go c.Run()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	log.Println("student: shutting down")
	c.Close()
}
