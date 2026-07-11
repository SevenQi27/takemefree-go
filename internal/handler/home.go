package handler

import (
	"embed"
	"html/template"
	"log"
	"net/http"
)

// 模板用 go:embed 编进二进制：单一制品，容器里不需要再拷模板目录。
//
//go:embed templates/home.html
var homeFS embed.FS

var homeTmpl = template.Must(template.ParseFS(homeFS, "templates/home.html"))

// Profile 个人介绍页数据。
type Profile struct {
	Username string
	Bio      string
	GitHub   string
	Stack    []string
	Projects []Project
}

type Project struct {
	Name string
	URL  string
	Desc string
}

var profile = Profile{
	Username: "SevenQi27",
	Bio:      "Java backend engineer moving toward crypto-exchange wallet/custody systems. Learning Go, one service at a time.",
	GitHub:   "https://github.com/SevenQi27",
	Stack:    []string{"Java / Spring Boot", "Go", "PostgreSQL / MySQL", "AWS (Lambda · ECS · CDK)", "BouncyCastle / PKI"},
	Projects: []Project{
		{"crypto-primitives-demo", "https://github.com/SevenQi27/crypto-primitives-demo", "Hands-on cryptography primitives with Java and BouncyCastle"},
		{"niuma-rescue", "https://github.com/SevenQi27/niuma-rescue", "Multi-agent dev pipeline on Feishu"},
		{"takemefree-go", "https://github.com/SevenQi27/takemefree-go", "This service: Node-to-Go migration exercise on AWS"},
	},
}

// Home GET / 渲染个人介绍页。
func Home(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := homeTmpl.Execute(w, profile); err != nil {
		log.Printf("渲染 home 模板失败: %v", err)
	}
}
