package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ReAct 循环：不依赖各家 function-calling 实现差异，用协议无关的 JSON 约定——
// 模型每轮只输出一个 JSON，要么 {"thought":..,"tool":..,"args":{}} 要么 {"thought":..,"final":"报告"}。
// 任何 OpenAI 兼容的 chat 端点（MiMo/DeepSeek/通义/Bedrock 网关）都能当大脑，
// 切换只动三个环境变量：LLM_BASE_URL / LLM_API_KEY / LLM_MODEL。

const maxSteps = 8

// StepEvent 循环过程事件，SSE 推给聊天页实时渲染
type StepEvent struct {
	Type        string `json:"type"` // thought / tool_call / tool_result / final / error
	Thought     string `json:"thought,omitempty"`
	Tool        string `json:"tool,omitempty"`
	Observation string `json:"observation,omitempty"`
	Text        string `json:"text,omitempty"`
}

type Agent struct {
	tb      *Toolbox
	baseURL string
	apiKey  string
	model   string
}

func New(tb *Toolbox) *Agent {
	return &Agent{
		tb:      tb,
		baseURL: strings.TrimRight(os.Getenv("LLM_BASE_URL"), "/"),
		apiKey:  os.Getenv("LLM_API_KEY"),
		model:   os.Getenv("LLM_MODEL"),
	}
}

func (a *Agent) Enabled() bool { return a.baseURL != "" && a.apiKey != "" && a.model != "" }

func (a *Agent) systemPrompt() string {
	var b strings.Builder
	b.WriteString(`你是 takemefree-go 服务的运维诊断 agent。你能调用工具查询这套 AWS 环境的真实状态（CloudWatch 告警、SQS 队列、Synthetics 巡检、接口性能、请求日志）。

规则：
1. 每轮回复只输出一个 JSON 对象，不要输出任何其他文字、不要用代码块包裹。
2. 需要查数据时输出：{"thought":"简述你要查什么和为什么","tool":"工具名","args":{}}
3. 信息足够下结论时输出：{"thought":"...","final":"给运维人员的中文诊断/回答，可用markdown"}
4. 结论必须基于工具返回的真实数据，不要编造；数据正常就明说正常。
5. 最多调用 8 次工具。

可用工具：
`)
	for _, t := range a.tb.Tools {
		fmt.Fprintf(&b, "- %s: %s\n", t.Name, t.Description)
	}
	return b.String()
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Run 执行诊断对话。emit 会在每个节点被调用（SSE 推流）；返回最终回答。
func (a *Agent) Run(ctx context.Context, question string, emit func(StepEvent)) (string, error) {
	if !a.Enabled() {
		return "", fmt.Errorf("agent 未启用：LLM_BASE_URL/LLM_API_KEY/LLM_MODEL 未配置")
	}
	messages := []chatMessage{
		{Role: "system", Content: a.systemPrompt()},
		{Role: "user", Content: question},
	}
	for step := 0; step < maxSteps; step++ {
		reply, err := a.chat(ctx, messages)
		if err != nil {
			return "", err
		}
		messages = append(messages, chatMessage{Role: "assistant", Content: reply})

		var decision struct {
			Thought string          `json:"thought"`
			Tool    string          `json:"tool"`
			Args    json.RawMessage `json:"args"`
			Final   string          `json:"final"`
		}
		if err := json.Unmarshal(extractJSON(reply), &decision); err != nil {
			// 模型没守 JSON 约定：把原文当最终回答兜底
			emit(StepEvent{Type: "final", Text: reply})
			return reply, nil
		}
		if decision.Final != "" {
			emit(StepEvent{Type: "final", Thought: decision.Thought, Text: decision.Final})
			return decision.Final, nil
		}
		tool := a.tb.Find(decision.Tool)
		if tool == nil {
			messages = append(messages, chatMessage{Role: "user", Content: fmt.Sprintf(`{"observation":"工具 %s 不存在，可用工具见系统提示"}`, decision.Tool)})
			continue
		}
		emit(StepEvent{Type: "tool_call", Thought: decision.Thought, Tool: decision.Tool})
		obs, err := tool.Run(ctx, decision.Args)
		if err != nil {
			obs = "工具执行失败: " + err.Error()
		}
		emit(StepEvent{Type: "tool_result", Tool: decision.Tool, Observation: obs})
		messages = append(messages, chatMessage{Role: "user", Content: "工具返回:\n" + obs})
	}
	return "", fmt.Errorf("超过最大工具调用次数(%d)仍未得出结论", maxSteps)
}

func (a *Agent) chat(ctx context.Context, messages []chatMessage) (string, error) {
	payload, _ := json.Marshal(map[string]any{
		"model":       a.model,
		"messages":    messages,
		"temperature": 0.2,
	})
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("模型调用失败 %d: %.300s", resp.StatusCode, body)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil || len(out.Choices) == 0 {
		return "", fmt.Errorf("模型响应解析失败: %.300s", body)
	}
	return out.Choices[0].Message.Content, nil
}

// extractJSON 宽容提取回复里的第一个完整 JSON 对象（有的模型爱带前后缀或代码块）
func extractJSON(s string) []byte {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return []byte(s)
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return []byte(s[start : i+1])
			}
		default:
		}
	}
	return []byte(s[start:])
}
