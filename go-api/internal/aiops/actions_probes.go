package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"forest/go-api/internal/admin"
)

type probeActionPayload struct {
	Selection        admin.AIEntryProbeSelection `json:"selection"`
	Targets          []admin.AIEntryProbeTarget  `json:"targets,omitempty"`
	Probes           []admin.AIEntryProbeAgent   `json:"probes,omitempty"`
	ExpectedResults  int                         `json:"expected_results"`
	AutomaticEffects string                      `json:"automatic_effects"`
}
type aiProbeCaptureService interface {
	CaptureAIEntryProbeSelection(context.Context, *sql.Tx, admin.AIEntryProbeSelection) (admin.AIEntryProbeSnapshot, error)
}

func (s *Service) captureProbeAction(ctx context.Context, tx *sql.Tx, spec actionSpec) (json.RawMessage, string, string, error) {
	var p probeActionPayload
	if spec.Kind != "entry_probe_run" || json.Unmarshal(spec.Payload, &p) != nil {
		return nil, "", "", ErrValidation
	}
	service, ok := s.panel.(aiProbeCaptureService)
	if !ok {
		return nil, "", "", ErrUnavailable
	}
	snapshot, err := service.CaptureAIEntryProbeSelection(ctx, tx, p.Selection)
	if err != nil {
		if errors.Is(err, admin.ErrAIEntryProbeConflict) {
			err = ErrConflict
		} else if errors.Is(err, admin.ErrAIEntryProbeSelection) {
			err = ErrValidation
		}
		return nil, "", "", err
	}
	p.Targets = snapshot.Targets
	p.Probes = snapshot.Probes
	p.ExpectedResults = len(p.Targets) * len(p.Probes)
	p.AutomaticEffects = "仅对列表中的目标与探针发起新 TCP 检测；探针上报仍可能触发现有自动切换或二分。已受理不代表检测通过，请按 run_id 查询结果。"
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > 64000 {
		return nil, "", "", ErrValidation
	}
	return raw, snapshot.Fingerprint, fmt.Sprintf("%d 个入口 × %d 个探针", len(p.Targets), len(p.Probes)), nil
}

func (s *Service) executeProbeAction(ctx context.Context, a Action, snapshot string) (status, output, problem string) {
	if err := s.requireOperator(ctx, a.ApprovedBy); err != nil {
		return "failed", "", "运维权限已变化，未发起检测"
	}
	var p probeActionPayload
	if json.Unmarshal(a.Payload, &p) != nil {
		return "failed", "", "检测参数不可用，未发送任务"
	}
	service, ok := s.panel.(interface {
		StartAIEntryProbeRun(context.Context, admin.AIEntryProbeSelection, int64, string, string) (int64, error)
	})
	if !ok {
		return "failed", "", "检测服务不可用，未发送任务"
	}
	id, err := service.StartAIEntryProbeRun(ctx, p.Selection, a.ApprovedBy, fmt.Sprintf("ai-entry-probe:%d", a.ID), snapshot)
	if err == nil {
		return "succeeded", fmt.Sprintf("检测任务已受理，run_id=%d；请使用 entry_probe_result 查询本次目标×探针结果。未上报不能判定正常；上报仍遵循原自动切换/二分设置。", id), ""
	}
	if errors.Is(err, admin.ErrAIEntryProbeConflict) || errors.Is(err, admin.ErrAIEntryProbeSelection) {
		return "failed", "", "入口或探针配置已变化／不可用，未发起检测，请重新查询并提议"
	}
	return "unknown", "", "检测发起结果未知，请核对已有检测任务，禁止自动重试"
}
