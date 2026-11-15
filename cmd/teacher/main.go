// Command teacher runs the 天眼 (TianYan) teacher server and web console.
package main

import (
	"flag"
	"log"

	"tianyan/internal/teacher"
)

func main() {
	cfg := teacher.DefaultConfig()
	flag.StringVar(&cfg.Name, "name", cfg.Name, "教师机名称")
	flag.IntVar(&cfg.ControlPort, "port", cfg.ControlPort, "学生控制 TCP 端口")
	flag.StringVar(&cfg.HTTPAddr, "http", cfg.HTTPAddr, "控制台 HTTP 监听地址")
	flag.StringVar(&cfg.DataDir, "data", cfg.DataDir, "数据/接收作业目录")
	flag.Parse()

	s, err := teacher.New(cfg)
	if err != nil {
		log.Fatalf("teacher: %v", err)
	}
	log.Printf("天眼教师端启动（控制台 http://localhost%s）", cfg.HTTPAddr)
	log.Fatal(s.Run())
}
