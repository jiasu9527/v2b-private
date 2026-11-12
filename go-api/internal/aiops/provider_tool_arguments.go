package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

type providerArgumentCorrectionKey struct{}

// A completion can visit multiple relays and models sequentially. Sharing the
// correction here prevents each model from receiving a fresh retry allowance.
type providerArgumentCorrectionState struct {
	failure *providerToolArgumentError
}

func withProviderArgumentCorrection(ctx context.Context) context.Context {
	return context.WithValue(ctx, providerArgumentCorrectionKey{}, &providerArgumentCorrectionState{})
}

// No arguments or arbitrary provider-supplied names may escape in diagnostics.
// A fragmented/unknown tool name is reported as unknown rather than echoed.
type providerToolArgumentError struct {
	ToolName string
	Bytes    int
	Limit    int
}

func (e *providerToolArgumentError) Error() string {
	return fmt.Sprintf("AI 工具参数过长（工具 %s，%d 字节，上限 %d 字节），本次工具未执行；请精简参数或分批操作", e.ToolName, e.Bytes, e.Limit)
}

var providerDiagnosticToolNames = sync.OnceValue(func() map[string]bool {
	names := map[string]bool{}
	for _, tool := range (&Service{}).toolDefinitions() {
		names[tool.Function.Name] = true
	}
	return names
})

func newProviderToolArgumentError(name string, size, limit int) error {
	if !providerDiagnosticToolNames()[name] {
		name = "unknown"
	}
	return &providerToolArgumentError{ToolName: name, Bytes: size, Limit: limit}
}

func isProviderToolArgumentError(err error) bool {
	var sizeErr *providerToolArgumentError
	return errors.As(err, &sizeErr)
}

// Correct only the not-yet-executed completion. Keep every original message,
// tool result and request setting intact; never insert the invalid tool call
// or a synthetic tool result (which would create an unmatched tool batch).
func correctProviderToolArguments(payload []byte, sizeErr *providerToolArgumentError) ([]byte, bool) {
	var request map[string]json.RawMessage
	if json.Unmarshal(payload, &request) != nil || request == nil {
		return nil, false
	}
	var messages []json.RawMessage
	if json.Unmarshal(request["messages"], &messages) != nil || len(messages) == 0 {
		return nil, false
	}
	prompt := fmt.Sprintf("后端参数校验反馈：上一条尚未执行的模型回复中，工具 %s 的参数已达到 %d 字节，超过 %d 字节上限，本次回复中的全部工具均未执行。此前历史中的已完成工具结果仍有效，禁止重新执行已完成动作。请继续完成当前用户原任务，只重新生成必要的下一步；不要复制历史消息、完整工具结果或大数组到参数里。使用邮箱、ID、筛选条件、分页或已返回的结果ID；大批量任务分批，任务记忆仅保留精简事实和约束。不要删除必要条件或更改目标来凑长度，不要截断JSON；无法安全缩短时说明具体原因。当前超限工具的字节上限见本条反馈，各字段仍须满足当前已声明工具schema及说明中的独立限制；不要调用当前工具列表未提供的能力。这是一次参数纠正，不是新的操作授权。", sizeErr.ToolName, sizeErr.Bytes, sizeErr.Limit)
	message, err := json.Marshal(providerMessage{Role: "system", Content: prompt})
	if err != nil {
		return nil, false
	}
	messages = append(messages, message)
	request["messages"], err = json.Marshal(messages)
	if err != nil {
		return nil, false
	}
	encoded, err := json.Marshal(request)
	return encoded, err == nil && len(encoded) <= maxProviderRequestBytes
}
