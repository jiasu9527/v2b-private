package aiops

import (
	"errors"
	"sort"
)

type completionToolDelta struct {
	Index    *int         `json:"index"`
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type completionToolBuilder struct {
	index *int
	call  ToolCall
}

type completionToolAssembler struct {
	indexed map[int]*completionToolBuilder
	ordered []*completionToolBuilder
}

var errStreamToolAssociation = errors.New("AI 中转返回的工具分片缺少可唯一匹配的编号或 ID，未执行本次工具")

func (a *completionToolAssembler) findID(id string) (*completionToolBuilder, error) {
	var found *completionToolBuilder
	if id == "" {
		return nil, nil
	}
	for _, candidate := range a.ordered {
		if candidate.call.ID == id {
			if found != nil {
				return nil, errStreamToolAssociation
			}
			found = candidate
		}
	}
	return found, nil
}

func (a *completionToolAssembler) append(delta completionToolDelta, frameCount int) error {
	var builder *completionToolBuilder
	if delta.Index != nil {
		if *delta.Index < 0 {
			return errors.New("AI 中转返回了负数工具编号，未执行本次工具")
		}
		builder = a.indexed[*delta.Index]
		if delta.ID != "" {
			// A relay may omit the index initially, then supply it with the
			// same full ID. Bind only that unique unindexed call, never merge
			// two distinct explicit indexes that happen to share an ID prefix.
			candidate, err := a.findID(delta.ID)
			if err != nil {
				return err
			}
			if builder != nil && candidate != nil && candidate != builder {
				// An explicit index and a known full ID must agree. Otherwise
				// appending the ID would hide a cross-call argument assignment.
				return errStreamToolAssociation
			}
			if builder == nil && candidate != nil && candidate.index == nil {
				builder = candidate
			}
		}
	} else if delta.ID != "" {
		var err error
		builder, err = a.findID(delta.ID)
		if err != nil {
			return err
		}
		if builder == nil && delta.Function.Name == "" {
			// Without an index, an unknown ID fragment has no reliable target.
			return errStreamToolAssociation
		}
	} else {
		// Do not infer from array position: a later delta often contains just
		// one of several interleaved calls. Multiple anonymous deltas within
		// one frame also cannot safely be combined into the same call.
		if len(a.ordered) != 1 || frameCount != 1 {
			return errStreamToolAssociation
		}
		builder = a.ordered[0]
	}
	if builder == nil {
		if len(a.ordered) >= maxCompletionToolCalls {
			return errors.New("AI 单次回复的工具调用过多，未执行工具；请分批操作后继续")
		}
		builder = &completionToolBuilder{}
		a.ordered = append(a.ordered, builder)
	}
	if delta.Index != nil && builder.index == nil {
		value := *delta.Index
		builder.index = &value
		if a.indexed == nil {
			a.indexed = map[int]*completionToolBuilder{}
		}
		a.indexed[value] = builder
	}
	call := &builder.call
	call.ID = appendToolIdentity(call.ID, delta.ID)
	call.Function.Name = appendToolIdentity(call.Function.Name, delta.Function.Name)
	// Arguments are deltas, including legitimately repeated text. Never
	// deduplicate or treat arguments as cumulative snapshots.
	call.Function.Arguments += delta.Function.Arguments
	if delta.Type != "" {
		if delta.Type != "function" || (call.Type != "" && call.Type != delta.Type) {
			return errors.New("AI 流式工具类型无效，未执行工具")
		}
		call.Type = delta.Type
	}
	// The name may still be fragmented. Apply its exact argument budget only
	// after the entire response has finished and passed validation.
	if len(call.Function.Arguments) > 32768 {
		return errors.New("AI 工具参数过长，未执行工具；请缩小单次参数或分批操作")
	}
	if len(call.ID) > 200 || len(call.Function.Name) > 100 {
		return errors.New("AI 工具请求超出单轮限制")
	}
	return nil
}

func appendToolIdentity(current, fragment string) string {
	// Some relays repeat the full ID/name on every argument delta. Other
	// fragments still append verbatim, even if they share a common prefix.
	if fragment == "" || current == fragment {
		return current
	}
	return current + fragment
}

func (a *completionToolAssembler) result() []ToolCall {
	ordered := append([]*completionToolBuilder(nil), a.ordered...)
	allIndexed := true
	for _, builder := range ordered {
		if builder.index == nil {
			allIndexed = false
			break
		}
	}
	if allIndexed {
		sort.Slice(ordered, func(i, j int) bool { return *ordered[i].index < *ordered[j].index })
	}
	out := make([]ToolCall, 0, len(ordered))
	for _, builder := range ordered {
		out = append(out, builder.call)
	}
	return out
}
