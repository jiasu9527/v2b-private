package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"forest/go-api/internal/aiops"
	"forest/go-api/internal/session"
)

type aiOpsDraftService interface {
	GetDraft(context.Context, int64, int64) (aiops.Draft, error)
}
type aiOpsSnapshot struct {
	aiops.SessionDetail
	Actions []aiops.Action `json:"actions"`
}

// Bound persistent browser connections independently from model generation.
var aiStreamClients = struct {
	sync.Mutex
	total  int
	actors map[int64]int
}{actors: make(map[int64]int)}

func acquireAIStream(actor int64) bool {
	aiStreamClients.Lock()
	defer aiStreamClients.Unlock()
	if aiStreamClients.total >= 64 || aiStreamClients.actors[actor] >= 4 {
		return false
	}
	aiStreamClients.total++
	aiStreamClients.actors[actor]++
	return true
}
func releaseAIStream(actor int64) {
	aiStreamClients.Lock()
	defer aiStreamClients.Unlock()
	aiStreamClients.total--
	aiStreamClients.actors[actor]--
	if aiStreamClients.actors[actor] == 0 {
		delete(aiStreamClients.actors, actor)
	}
}

func loadAIOpsSnapshot(ctx context.Context, s aiOpsService, tools aiOpsToolsService, actor, id int64) (aiOpsSnapshot, error) {
	detail, err := s.GetSession(ctx, actor, id)
	if err != nil {
		return aiOpsSnapshot{}, err
	}
	actions, err := tools.ListActions(ctx, actor, id)
	return aiOpsSnapshot{SessionDetail: detail, Actions: actions}, err
}
func snapshotRunning(v aiOpsSnapshot) bool {
	if v.Session.Status == "running" {
		return true
	}
	for _, a := range v.Actions {
		if a.Status == "running" {
			return true
		}
	}
	return false
}

func handleAIOpsStream(w http.ResponseWriter, r *http.Request, sessions session.Service, s aiOpsService, actor int64) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil || id <= 0 {
		handleAIOpsError(w, aiops.ErrValidation)
		return
	}
	drafts, ok := s.(aiOpsDraftService)
	tools, hasTools := s.(aiOpsToolsService)
	if !ok || !hasTools {
		handleAIOpsError(w, aiops.ErrUnavailable)
		return
	}
	if !acquireAIStream(actor) {
		handleAIOpsError(w, aiops.ErrBusy)
		return
	}
	defer releaseAIStream(actor)
	ctx, cancel := context.WithTimeout(r.Context(), 28*time.Second)
	defer cancel()
	control := http.NewResponseController(w)
	_ = control.SetWriteDeadline(time.Now().Add(32 * time.Second))
	initial, err := loadAIOpsSnapshot(ctx, s, tools, actor, id)
	if err != nil {
		handleAIOpsError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeEvent := func(name string, value any) bool {
		raw, err := json.Marshal(value)
		if err != nil {
			return false
		}
		if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, raw); err != nil {
			return false
		}
		return control.Flush() == nil
	}
	stop := func() { writeEvent("end", map[string]int64{"session_id": id}) }
	if !writeEvent("snapshot", initial) {
		return
	}
	if !snapshotRunning(initial) {
		stop()
		return
	}
	initialRaw, _ := json.Marshal(initial)
	lastHash := sha256.Sum256(initialRaw)
	lastFull := time.Now()
	lastDraft := aiops.Draft{}
	ticker := time.NewTicker(600 * time.Millisecond)
	defer ticker.Stop()
	lifetime := time.NewTimer(25 * time.Second)
	defer lifetime.Stop()
	token := strings.TrimSpace(r.Header.Get("Authorization"))
	if token == "" {
		token = strings.TrimSpace(r.URL.Query().Get("auth_data"))
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-lifetime.C:
			stop()
			return
		case <-ticker.C:
			// Mini App tokens are scoped to a live Telegram binding. Revalidate
			// before every emitted draft, not only when loading a full snapshot.
			if scoped, ok := sessions.(interface{ RevalidateEveryStreamTick() bool }); ok && scoped.RevalidateEveryStreamTick() {
				identity, authErr := sessions.Authenticate(ctx, token, false)
				if authErr != nil || identity == nil || identity.ID != actor || (identity.IsAdmin == 0 && identity.IsStaff == 0) {
					writeEvent("error", map[string]any{"session_id": id, "code": "auth_invalid", "message": "Telegram 运维授权已失效，请从 Bot 重新打开"})
					return
				}
			}
			draft, err := drafts.GetDraft(ctx, actor, id)
			if err != nil {
				writeEvent("error", map[string]any{"session_id": id, "message": "会话不存在、权限已变化或连接暂不可用，请刷新核对"})
				return
			}
			// A cleared draft can mean final messages were published while the
			// turn is still running. Send that snapshot before clearing the UI.
			draftCleared := draft.Content == "" && lastDraft.Content != "" && draft.Version > lastDraft.Version
			fullDue := draftCleared || time.Since(lastFull) >= 3*time.Second || draft.TurnID != lastDraft.TurnID || draft.Round != lastDraft.Round || draft.Status != lastDraft.Status
			if fullDue {
				// Fresh token validation catches session revocation, not only role changes.
				var identity *session.Identity
				if fresh, ok := sessions.(freshSessionService); ok {
					identity, err = fresh.AuthenticateFresh(ctx, token, false)
				} else {
					identity, err = sessions.Authenticate(ctx, token, false)
				}
				if err != nil || identity == nil || identity.ID != actor || (identity.IsAdmin == 0 && identity.IsStaff == 0) {
					writeEvent("error", map[string]any{"session_id": id, "message": "运维登录会话已失效，请重新登录"})
					return
				}
				snapshot, err := loadAIOpsSnapshot(ctx, s, tools, actor, id)
				if err != nil {
					writeEvent("error", map[string]any{"session_id": id, "message": "会话数据已变化，请刷新"})
					return
				}
				raw, _ := json.Marshal(snapshot)
				hash := sha256.Sum256(raw)
				if hash != lastHash {
					if !writeEvent("snapshot", snapshot) {
						return
					}
					lastHash = hash
				}
				lastFull = time.Now()
				if !snapshotRunning(snapshot) {
					stop()
					return
				}
			}
			if draft != lastDraft {
				if draft.Status == "running" && !writeEvent("draft", draft) {
					return
				}
				lastDraft = draft
			}
		}
	}
}
