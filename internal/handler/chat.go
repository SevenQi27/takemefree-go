package handler

import (
	"embed"
	"html/template"
	"log"
	"net/http"
)

//go:embed templates/chat.html
var chatFS embed.FS

var chatTmpl = template.Must(template.ParseFS(chatFS, "templates/chat.html"))

// ChatPage GET /ops/chat —— AI 运维对话页（作业2 门面A）
func ChatPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := chatTmpl.Execute(w, nil); err != nil {
		log.Printf("chat page: %v", err)
	}
}
