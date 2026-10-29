package aiops

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

// Token-by-token JSON framing can be several times larger than the resulting
// answer. Keep a separate bounded wire budget; public text and tool limits are
// unchanged from non-streaming completions.
const maxCompletionStreamBytes = 2 << 20
const maxCompletionEventBytes = 256 << 10

type completionDelta struct {
	Choices []struct {
		Index *int `json:"index"`
		Delta struct {
			Role      string                `json:"role"`
			Content   *string               `json:"content"`
			ToolCalls []completionToolDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Error json.RawMessage `json:"error"`
}

func readCompletionStream(ctx context.Context, reader io.Reader, onContent func(string) error) (providerMessage, error) {
	empty := providerMessage{}
	out := providerMessage{Role: "assistant"}
	calls := completionToolAssembler{}
	finished, done := false, false
	finishReason := ""
	limited := &io.LimitedReader{R: reader, N: maxCompletionStreamBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), maxCompletionEventBytes+1)
	var data strings.Builder
	eventType := ""
	firstLine := true
	consume := func() error {
		payload := data.String()
		data.Reset()
		kind := eventType
		eventType = ""
		if kind == "error" {
			return errors.New("AI 流式响应返回错误，未执行工具")
		}
		if payload == "" {
			return nil
		}
		payload = strings.TrimSuffix(payload, "\n")
		if payload == "[DONE]" {
			if !finished {
				return errors.New("AI 流式响应缺少完成状态，未执行工具")
			}
			done = true
			return nil
		}
		if !utf8.ValidString(payload) {
			return errors.New("AI 流式响应编码无效，未执行工具")
		}
		var frame completionDelta
		if json.Unmarshal([]byte(payload), &frame) != nil || (len(frame.Error) != 0 && string(frame.Error) != "null") {
			return errors.New("AI 流式响应格式无效或返回错误，未执行工具")
		}
		// OpenAI-compatible usage frames have choices=[] after the final delta.
		if len(frame.Choices) == 0 {
			return nil
		}
		if len(frame.Choices) != 1 {
			return errors.New("AI 流式响应包含非预期候选，未执行工具")
		}
		choice := frame.Choices[0]
		if choice.Index != nil && *choice.Index != 0 {
			return errors.New("AI 流式响应候选编号无效，未执行工具")
		}
		if finished {
			return errors.New("AI 流式响应完成后仍有内容，未执行工具")
		}
		if choice.Delta.Role != "" && choice.Delta.Role != "assistant" {
			return errors.New("AI 流式响应角色无效，未执行工具")
		}
		changed := choice.Delta.Content != nil && *choice.Delta.Content != ""
		if changed {
			out.Content += *choice.Delta.Content
			if len(out.Content) > 32000 {
				return errors.New("AI 响应超出单轮限制")
			}
		}
		for _, delta := range choice.Delta.ToolCalls {
			if err := calls.append(delta, len(choice.Delta.ToolCalls)); err != nil {
				return err
			}
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			finishReason = *choice.FinishReason
			if finishReason != "stop" && finishReason != "tool_calls" {
				return errors.New("AI 流式响应未完整结束，未执行工具；请检查模型输出限制")
			}
			finished = true
		}
		if changed && onContent != nil {
			if err := onContent(out.Content); err != nil {
				return errProviderContentUpdate
			}
		}
		return nil
	}
	for scanner.Scan() {
		if ctx.Err() != nil {
			return empty, errors.New("AI 流式响应已取消，未执行工具")
		}
		if limited.N <= 0 {
			return empty, errors.New("AI 流式响应超出大小限制，未执行工具")
		}
		line := scanner.Text()
		if firstLine {
			line = strings.TrimPrefix(line, "\ufeff")
			firstLine = false
		}
		if line == "" {
			if err := consume(); err != nil {
				return empty, err
			}
			if done {
				break
			}
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			if data.Len()+len(value)+1 > maxCompletionEventBytes {
				return empty, errors.New("AI 流式事件超出大小限制，未执行工具")
			}
			data.WriteString(value)
			data.WriteByte('\n')
		case "event":
			eventType = value
		}
	}
	if scanner.Err() != nil || limited.N <= 0 {
		return empty, errors.New("AI 流式响应中断或超出限制，未执行工具")
	}
	if !done && data.Len() > 0 {
		if err := consume(); err != nil {
			return empty, err
		}
	}
	if !done || !finished || ctx.Err() != nil {
		return empty, errors.New("AI 流式响应未完整结束，未执行工具")
	}
	out.ToolCalls = calls.result()
	if (len(out.ToolCalls) > 0 && finishReason != "tool_calls") || (len(out.ToolCalls) == 0 && finishReason != "stop") {
		return empty, errors.New("AI 流式完成状态与工具请求不匹配，未执行工具")
	}
	if err := validateCompletion(out); err != nil {
		return empty, err
	}
	return out, nil
}
