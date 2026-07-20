package agent

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
)

// 门面B：把同一份工具核心以 MCP 协议（Streamable HTTP 传输）暴露给外部 agent 宿主。
// Claude Code 接入：claude mcp add --transport http ops http://<ALB>/mcp --header "Authorization: Bearer <token>"
// 实现的是最小合规子集：initialize / notifications/initialized / ping / tools/list / tools/call，
// 全部单次 JSON 响应（Streamable HTTP 允许服务端不开 SSE 流）。

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func rpcResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func rpcFail(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": rpcError{Code: code, Message: msg}})
}

// MCPHandler POST /mcp
func (a *Agent) MCPHandler(w http.ResponseWriter, r *http.Request) {
	token := os.Getenv("MCP_AUTH_TOKEN")
	if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		rpcFail(w, nil, -32700, "parse error")
		return
	}
	log.Printf("mcp: %s", req.Method)

	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		rpcResult(w, req.ID, map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "takemefree-ops", "version": "1.0.0"},
		})
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "ping":
		rpcResult(w, req.ID, map[string]any{})
	case "tools/list":
		rpcResult(w, req.ID, map[string]any{"tools": a.tb.Tools})
	case "tools/call":
		var p struct {
			Name string          `json:"name"`
			Args json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			rpcFail(w, req.ID, -32602, "invalid params")
			return
		}
		tool := a.tb.Find(p.Name)
		if tool == nil {
			rpcFail(w, req.ID, -32602, "unknown tool: "+p.Name)
			return
		}
		text, err := tool.Run(r.Context(), p.Args)
		isErr := false
		if err != nil {
			text, isErr = err.Error(), true
		}
		rpcResult(w, req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"isError": isErr,
		})
	default:
		rpcFail(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

// ChatHandler POST /api/ops/chat —— 聊天页的 SSE 出口：过程事件实时推流
func (a *Agent) ChatHandler(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Question string `json:"question"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Question == "" {
		http.Error(w, `{"error":"question required"}`, http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	emit := func(ev StepEvent) {
		data, _ := json.Marshal(ev)
		_, _ = w.Write([]byte("data: " + string(data) + "\n\n"))
		flusher.Flush()
	}
	if _, err := a.Run(r.Context(), in.Question, emit); err != nil {
		emit(StepEvent{Type: "error", Text: err.Error()})
	}
}
