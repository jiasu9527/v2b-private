package httpapi

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"forest/go-api/internal/guest"
	"forest/go-api/internal/nodeapi"
	"forest/go-api/internal/passport"
	"forest/go-api/internal/payment"
	"forest/go-api/internal/queue"
	"forest/go-api/internal/session"
	usersvc "forest/go-api/internal/user"
)

type Option func(*routerState)

type telegramWebhookService interface {
	HandleWebhook(ctx context.Context, payload map[string]any) error
}

type routerState struct {
	readyCheck        func(context.Context) error
	runtime           *config.RuntimeState
	guest             guest.Service
	passport          passport.Service
	session           session.Service
	user              usersvc.Service
	payment           payment.Service
	admin             admin.Service
	node              nodeapi.Service
	jobs              queue.Enqueuer
	telegram          telegramWebhookService
	clientEntryRemote clientEntryRemoteResolver
}

var managedServerRouterTypes = map[string]struct{}{
	"shadowsocks": {},
	"vmess":       {},
	"vless":       {},
	"trojan":      {},
	"tuic":        {},
	"hysteria":    {},
	"anytls":      {},
	"v2node":      {},
}

func WithReadyCheck(fn func(context.Context) error) Option {
	return func(state *routerState) {
		state.readyCheck = fn
	}
}

func WithRuntimeConfig(runtime *config.RuntimeState) Option {
	return func(state *routerState) {
		state.runtime = runtime
	}
}

func WithGuestService(service guest.Service) Option {
	return func(state *routerState) {
		state.guest = service
	}
}

func WithPassportService(service passport.Service) Option {
	return func(state *routerState) {
		state.passport = service
	}
}

func WithSessionService(service session.Service) Option {
	return func(state *routerState) {
		state.session = service
	}
}

func WithUserService(service usersvc.Service) Option {
	return func(state *routerState) {
		state.user = service
	}
}

func WithPaymentService(service payment.Service) Option {
	return func(state *routerState) {
		state.payment = service
	}
}

func WithAdminService(service admin.Service) Option {
	return func(state *routerState) {
		state.admin = service
	}
}

func WithNodeService(service nodeapi.Service) Option {
	return func(state *routerState) {
		state.node = service
	}
}

func WithQueueRuntime(jobs queue.Enqueuer) Option {
	return func(state *routerState) {
		state.jobs = jobs
	}
}

func WithTelegramService(service telegramWebhookService) Option {
	return func(state *routerState) {
		state.telegram = service
	}
}

func WithClientEntryRemoteResolver(resolver clientEntryRemoteResolver) Option {
	return func(state *routerState) {
		state.clientEntryRemote = resolver
	}
}

func NewRouter(cfg config.Config, options ...Option) http.Handler {
	state := &routerState{
		clientEntryRemote: newHTTPClientEntryRemoteResolver(),
	}
	for _, option := range options {
		option(state)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok",
			"app":    cfg.AppName,
		})
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ready := true
		if state.readyCheck != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			ready = state.readyCheck(ctx) == nil
		}

		status := http.StatusOK
		if !ready {
			status = http.StatusServiceUnavailable
		}

		writeJSON(w, status, map[string]any{
			"status":              map[bool]string{true: "ready", false: "not_ready"}[ready],
			"postgres_configured": cfg.PostgresDSN != "",
		})
	})
	mux.HandleFunc("/api/_meta/runtime", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"app":        cfg.AppName,
			"addr":       cfg.Addr,
			"admin_path": cfg.AdminPath,
			"postgres":   cfg.PostgresDSN != "",
			"public_dir": cfg.PublicDir,
		})
	})
	mux.HandleFunc("/monitor/api/stats", func(w http.ResponseWriter, r *http.Request) {
		snapshot := queue.Snapshot{}
		if state.jobs != nil {
			snapshot = state.jobs.Snapshot()
		}
		status := "stopped"
		if snapshot.Running {
			status = "running"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":       status,
			"workers":      snapshot.Workers,
			"current_jobs": snapshot.CurrentJobs,
		})
	})
	fileServer := http.FileServer(http.Dir(cfg.PublicDir))

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := cfg
		if state.runtime != nil {
			cfg = state.runtime.CurrentConfig()
		}
		adminPrefix := "/api/v1/" + strings.Trim(strings.TrimSpace(cfg.AdminPath), "/")

		switch {
		case r.URL.Path == "/healthz", r.URL.Path == "/readyz", r.URL.Path == "/api/_meta/runtime", r.URL.Path == "/monitor/api/stats":
			mux.ServeHTTP(w, r)
			return
		case r.URL.Path == "/api/v1/guest/comm/config":
			if handleGuestConfig(w, r, state.guest) {
				return
			}
		case r.URL.Path == "/api/v1/guest/plan/fetch":
			if handleGuestPlans(w, r, state.guest) {
				return
			}
		case r.URL.Path == "/api/v1/guest/invite/preview":
			if handleGuestInvitePreview(w, r, state.guest) {
				return
			}
		case r.URL.Path == "/api/v1/guest/telegram/webhook":
			if handleGuestTelegramWebhook(w, r, cfg, state.telegram) {
				return
			}
		case strings.HasPrefix(r.URL.Path, "/api/v1/guest/payment/notify/"):
			if handleGuestPaymentNotify(w, r, state.payment) {
				return
			}
		case r.URL.Path == "/api/v1/server/UniProxy/user":
			if handleServerUniProxyUser(w, r, cfg, state.node) {
				return
			}
		case r.URL.Path == "/api/v1/server/UniProxy/config":
			if handleServerUniProxyConfig(w, r, cfg, state.node) {
				return
			}
		case r.URL.Path == "/api/v1/server/UniProxy/alivelist":
			if handleServerUniProxyAliveList(w, r, cfg, state.node) {
				return
			}
		case r.URL.Path == "/api/v1/server/UniProxy/alive":
			if handleServerUniProxyAlive(w, r, cfg, state.node) {
				return
			}
		case r.URL.Path == "/api/v1/server/UniProxy/push":
			if handleServerUniProxyPush(w, r, cfg, state.node) {
				return
			}
		case r.URL.Path == "/api/v1/server/Deepbwork/user":
			if handleServerDeepbworkUser(w, r, cfg, state.node) {
				return
			}
		case r.URL.Path == "/api/v1/server/Deepbwork/config":
			if handleServerDeepbworkConfig(w, r, cfg, state.node) {
				return
			}
		case r.URL.Path == "/api/v1/server/Deepbwork/submit":
			if handleServerLegacyTrafficSubmit(w, r, cfg, state.node, "vmess") {
				return
			}
		case r.URL.Path == "/api/v1/server/ShadowsocksTidalab/user":
			if handleServerShadowsocksUser(w, r, cfg, state.node) {
				return
			}
		case r.URL.Path == "/api/v1/server/ShadowsocksTidalab/submit":
			if handleServerLegacyTrafficSubmit(w, r, cfg, state.node, "shadowsocks") {
				return
			}
		case r.URL.Path == "/api/v1/server/TrojanTidalab/user":
			if handleServerTrojanUser(w, r, cfg, state.node) {
				return
			}
		case r.URL.Path == "/api/v1/server/TrojanTidalab/config":
			if handleServerTrojanConfig(w, r, cfg, state.node) {
				return
			}
		case r.URL.Path == "/api/v1/server/TrojanTidalab/submit":
			if handleServerLegacyTrafficSubmit(w, r, cfg, state.node, "trojan") {
				return
			}
		case r.URL.Path == "/api/v1/passport/comm/pv":
			if handlePassportPV(w, r, state.passport) {
				return
			}
		case r.URL.Path == "/api/v1/passport/comm/sendEmailVerify":
			if handlePassportSendEmailVerify(w, r, state.passport) {
				return
			}
		case r.URL.Path == "/api/v1/passport/auth/register":
			if handlePassportRegister(w, r, state.passport) {
				return
			}
		case r.URL.Path == "/api/v1/passport/auth/login":
			if handlePassportLogin(w, r, state.passport) {
				return
			}
		case r.URL.Path == "/api/v1/passport/auth/forget":
			if handlePassportForget(w, r, state.passport) {
				return
			}
		case r.URL.Path == "/api/v1/passport/auth/token2Login":
			if handlePassportToken2Login(w, r, state.passport) {
				return
			}
		case r.URL.Path == "/api/v1/passport/auth/getQuickLoginUrl":
			if handlePassportGetQuickLoginURL(w, r, state.passport) {
				return
			}
		case r.URL.Path == "/api/v1/client/app/getVersion":
			if handleClientAppGetVersion(w, r, cfg) {
				return
			}
		case r.URL.Path == "/api/v1/client/app/getConfig":
			if handleClientAppGetConfig(w, r, cfg, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/client/forest/entry-provider":
			if handleClientForestEntryProvider(w, r, cfg, state.user, state.clientEntryRemote) {
				return
			}
		case isClientSubscribePath(cfg, r.URL.Path):
			if handleClientSubscribe(w, r, cfg, state.user) {
				return
			}
		case r.URL.Path == "/api/v2/server/config":
			if handleServerV2Config(w, r, cfg, state.node) {
				return
			}
		case r.URL.Path == "/api/v1/user/checkLogin":
			if handleUserCheckLogin(w, r, state.session) {
				return
			}
		case r.URL.Path == "/api/v1/user/info":
			if handleUserInfo(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/getSubscribe":
			if handleUserGetSubscribe(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/forest/runtime-profile":
			if handleUserForestRuntimeProfile(w, r, cfg, state.session, state.user, state.clientEntryRemote) {
				return
			}
		case r.URL.Path == "/api/v1/user/unbindTelegram":
			if handleUserUnbindTelegram(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/resetSecurity":
			if handleUserResetSecurity(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/update":
			if handleUserUpdate(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/changePassword":
			if handleUserChangePassword(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/transfer":
			if handleUserTransfer(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/newPeriod":
			if handleUserNewPeriod(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/redeemgiftcard":
			if handleUserRedeemGiftcard(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/server/fetch":
			if handleUserServerFetch(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/plan/fetch":
			if handleUserPlanFetch(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/notice/fetch":
			if handleUserNoticeFetch(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/invite/save":
			if handleUserInviteSave(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/invite/fetch":
			if handleUserInviteFetch(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/invite/details":
			if handleUserInviteDetails(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/invite/campaign/save":
			if handleUserInviteCampaignSave(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/invite/campaign/fetch":
			if handleUserInviteCampaignFetch(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/invite/campaign/records":
			if handleUserInviteCampaignRecords(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/invite/campaign/abandon":
			if handleUserInviteCampaignAbandon(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/ticket/fetch":
			if handleUserTicketFetch(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/ticket/save":
			if handleUserTicketSave(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/ticket/reply":
			if handleUserTicketReply(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/ticket/close":
			if handleUserTicketClose(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/ticket/withdraw":
			if handleUserTicketWithdraw(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/comm/config":
			if handleUserCommConfig(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/comm/getStripePublicKey":
			if handleUserCommGetStripePublicKey(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/coupon/check":
			if handleUserCouponCheck(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/telegram/getBotInfo":
			if handleUserTelegramGetBotInfo(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/knowledge/fetch":
			if handleUserKnowledgeFetch(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/knowledge/getCategory":
			if handleUserKnowledgeGetCategory(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/stat/getTrafficLog":
			if handleUserStatGetTrafficLog(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/getQuickLoginUrl":
			if handlePassportGetQuickLoginURL(w, r, state.passport) {
				return
			}
		case r.URL.Path == "/api/v1/user/getActiveSession":
			if handleUserGetActiveSession(w, r, state.session) {
				return
			}
		case r.URL.Path == "/api/v1/user/getStat":
			if handleUserGetStat(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/removeActiveSession":
			if handleUserRemoveActiveSession(w, r, state.session) {
				return
			}
		case r.URL.Path == "/api/v1/user/order/fetch":
			if handleUserOrderFetch(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/order/save":
			if handleUserOrderSave(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/order/detail":
			if handleUserOrderDetail(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/order/checkout":
			if handleUserOrderCheckout(w, r, state.session, state.payment) {
				return
			}
		case r.URL.Path == "/api/v1/user/order/check":
			if handleUserOrderCheck(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/order/getPaymentMethod":
			if handleUserOrderGetPaymentMethod(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/user/order/cancel":
			if handleUserOrderCancel(w, r, state.session, state.user) {
				return
			}
		case r.URL.Path == "/api/v1/staff/plan/fetch":
			if handleStaffPlanFetch(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == "/api/v1/staff/notice/fetch":
			if handleStaffNoticeFetch(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == "/api/v1/staff/notice/save", r.URL.Path == "/api/v1/staff/notice/update":
			if handleStaffNoticeSave(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == "/api/v1/staff/notice/drop":
			if handleStaffNoticeDrop(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == "/api/v1/staff/ticket/fetch":
			if handleStaffTicketFetch(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == "/api/v1/staff/ticket/reply":
			if handleStaffTicketReply(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == "/api/v1/staff/ticket/close":
			if handleStaffTicketClose(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == "/api/v1/staff/user/getUserInfoById":
			if handleStaffUserGetInfo(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == "/api/v1/staff/user/update":
			if handleStaffUserUpdate(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == "/api/v1/staff/user/sendMail":
			if handleStaffUserSendMail(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == "/api/v1/staff/user/ban":
			if handleStaffUserBan(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/system/getSystemStatus":
			if handleAdminSystemStatus(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/stat/getOverride":
			if handleAdminStatOverride(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/stat/getStat":
			if handleAdminStatGetStat(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/stat/getOrder":
			if handleAdminStatOrder(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/stat/getRanking":
			if handleAdminStatGetRanking(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/stat/getStatRecord":
			if handleAdminStatGetStatRecord(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/stat/getServerLastRank":
			if handleAdminStatServerLastRank(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/stat/getServerTodayRank":
			if handleAdminStatServerTodayRank(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/stat/getInviteLastRank":
			if handleAdminStatInviteLastRank(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/stat/getInviteTodayRank":
			if handleAdminStatInviteTodayRank(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/stat/getUserLastRank":
			if handleAdminStatUserLastRank(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/stat/getUserTodayRank":
			if handleAdminStatUserTodayRank(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/stat/getStatUser":
			if handleAdminStatUser(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/user/fetch":
			if handleAdminUserFetch(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/user/getUserInfoById":
			if handleAdminUserGetInfo(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/user/update":
			if handleAdminUserUpdate(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/user/setInviteUser":
			if handleAdminUserSetInviteUser(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/user/generate":
			if handleAdminUserGenerate(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/user/dumpCSV":
			if handleAdminUserDumpCSV(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/user/sendMail":
			if handleAdminUserSendMail(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/user/ban":
			if handleAdminUserBan(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/user/resetSecret":
			if handleAdminUserResetSecret(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/user/delUser":
			if handleAdminUserDelUser(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/user/allDel":
			if handleAdminUserAllDel(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/server/group/fetch":
			if handleAdminServerGroupFetch(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/server/group/save":
			if handleAdminServerGroupSave(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/server/group/drop":
			if handleAdminServerGroupDrop(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/server/route/fetch":
			if handleAdminServerRouteFetch(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/server/route/save":
			if handleAdminServerRouteSave(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/server/route/drop":
			if handleAdminServerRouteDrop(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/server/client-entry/fetch":
			if handleAdminClientEntryGroupFetch(w, r, state.session, state.admin, state.clientEntryRemote) {
				return
			}
		case r.URL.Path == adminPrefix+"/server/client-entry/resolve":
			if handleAdminClientEntryGroupResolve(w, r, state.session, state.admin, state.clientEntryRemote) {
				return
			}
		case r.URL.Path == adminPrefix+"/server/client-entry/save":
			if handleAdminClientEntryGroupSave(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/server/client-entry/drop":
			if handleAdminClientEntryGroupDrop(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/server/manage/getNodes":
			if handleAdminServerManageGetNodes(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/server/manage/sort":
			if handleAdminServerManageSort(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/server/manage/updateHost":
			if handleAdminServerManageUpdateHost(w, r, state.session, state.admin) {
				return
			}
		case strings.HasPrefix(r.URL.Path, adminPrefix+"/server/"):
			if handleAdminManagedServerAction(w, r, state.session, state.admin, strings.TrimPrefix(r.URL.Path, adminPrefix+"/server/")) {
				return
			}
		case r.URL.Path == adminPrefix+"/config/fetch":
			if handleAdminConfigFetch(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/config/save":
			if handleAdminConfigSave(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/config/getEmailTemplate":
			if handleAdminConfigEmailTemplate(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/config/setTelegramWebhook":
			if handleAdminConfigSetTelegramWebhook(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/config/testSendMail":
			if handleAdminConfigTestSendMail(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/plan/fetch":
			if handleAdminPlanFetch(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/plan/save":
			if handleAdminPlanSave(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/plan/drop":
			if handleAdminPlanDrop(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/plan/update":
			if handleAdminPlanUpdate(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/plan/sort":
			if handleAdminPlanSort(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/notice/fetch":
			if handleAdminNoticeFetch(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/notice/save", r.URL.Path == adminPrefix+"/notice/update":
			if handleAdminNoticeSave(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/notice/drop":
			if handleAdminNoticeDrop(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/notice/show":
			if handleAdminNoticeShow(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/coupon/fetch":
			if handleAdminCouponFetch(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/coupon/generate":
			if handleAdminCouponGenerate(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/coupon/drop":
			if handleAdminCouponDrop(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/coupon/show":
			if handleAdminCouponShow(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/giftcard/fetch":
			if handleAdminGiftcardFetch(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/giftcard/generate":
			if handleAdminGiftcardGenerate(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/giftcard/drop":
			if handleAdminGiftcardDrop(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/knowledge/fetch":
			if handleAdminKnowledgeFetch(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/knowledge/getCategory":
			if handleAdminKnowledgeGetCategory(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/knowledge/save":
			if handleAdminKnowledgeSave(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/knowledge/show":
			if handleAdminKnowledgeShow(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/knowledge/drop":
			if handleAdminKnowledgeDrop(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/knowledge/sort":
			if handleAdminKnowledgeSort(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/ticket/fetch":
			if handleAdminTicketFetch(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/ticket/reply":
			if handleAdminTicketReply(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/ticket/close":
			if handleAdminTicketClose(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/order/fetch":
			if handleAdminOrderFetch(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/order/detail":
			if handleAdminOrderDetail(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/order/update":
			if handleAdminOrderUpdate(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/order/paid":
			if handleAdminOrderPaid(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/order/cancel":
			if handleAdminOrderCancel(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/order/refund":
			if handleAdminOrderRefund(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/order/assign":
			if handleAdminOrderAssign(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/invite/campaign/fetch":
			if handleAdminInviteCampaignFetch(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/invite/campaign/detail":
			if handleAdminInviteCampaignDetail(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/invite/campaign/records":
			if handleAdminInviteCampaignRecords(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/payment/fetch":
			if handleAdminPaymentFetch(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/payment/getPaymentMethods":
			if handleAdminPaymentMethods(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/payment/getPaymentForm":
			if handleAdminPaymentForm(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/payment/save":
			if handleAdminPaymentSave(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/payment/drop":
			if handleAdminPaymentDrop(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/payment/show":
			if handleAdminPaymentShow(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/payment/sort":
			if handleAdminPaymentSort(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/system/getQueueStats":
			if handleAdminQueueStats(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/system/getQueueWorkload", r.URL.Path == adminPrefix+"/system/getQueueMasters":
			if handleAdminQueueWorkload(w, r, state.session, state.admin) {
				return
			}
		case r.URL.Path == adminPrefix+"/system/getSystemLog":
			if handleAdminSystemLog(w, r, state.session, state.admin) {
				return
			}
		case maybeServeUIPage(cfg, w, r):
			return
		case shouldServeStatic(cfg.PublicDir, r.URL.Path):
			fileServer.ServeHTTP(w, r)
			return
		default:
			if shouldReturnForbiddenUIPage(r) {
				if !closeUIConnection(w) {
					writePlainText(w, http.StatusForbidden, "连接已关闭")
				}
				return
			}
			http.NotFound(w, r)
			return
		}
	})

	return withMiddleware(cfg, handler)
}

func clientSubscribePath(cfg config.Config) string {
	return normalizeClientSubscribePath(cfg.SubscribePath)
}

func normalizeClientSubscribePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "/api/v1/client/subscribe"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if len(path) > 1 {
		path = strings.TrimRight(path, "/")
	}
	if path == "" {
		return "/api/v1/client/subscribe"
	}
	return path
}

func isClientSubscribePath(cfg config.Config, path string) bool {
	_, ok := clientSubscribePathToken(cfg, path)
	return ok
}

func clientSubscribePathToken(cfg config.Config, path string) (string, bool) {
	defaultPath := "/api/v1/client/subscribe"
	customPath := clientSubscribePath(cfg)
	paths := []string{customPath}
	if customPath != defaultPath {
		paths = append(paths, defaultPath)
	}
	for _, candidate := range paths {
		candidate = normalizeClientSubscribePath(candidate)
		if path == candidate || path == candidate+"/" {
			return "", true
		}
		prefix := candidate + "/"
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		rawToken := strings.TrimPrefix(path, prefix)
		if rawToken == "" || strings.Contains(rawToken, "/") {
			return "", false
		}
		token, err := url.PathUnescape(rawToken)
		if err != nil {
			return "", false
		}
		return token, true
	}
	return "", false
}

func handleGuestConfig(w http.ResponseWriter, r *http.Request, service guest.Service) bool {
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "guest service unavailable"})
		return true
	}

	payload, err := service.Config(r.Context())
	if err != nil {
		if errors.Is(err, guest.ErrUnavailable) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "guest service unavailable"})
			return true
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": payload})
	return true
}

func handleGuestPlans(w http.ResponseWriter, r *http.Request, service guest.Service) bool {
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "guest service unavailable"})
		return true
	}

	payload, err := service.Plans(r.Context())
	if err != nil {
		if errors.Is(err, guest.ErrUnavailable) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "guest service unavailable"})
			return true
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": payload})
	return true
}

func handleGuestInvitePreview(w http.ResponseWriter, r *http.Request, service guest.Service) bool {
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "guest service unavailable"})
		return true
	}

	payload, err := service.InvitePreview(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		if errors.Is(err, guest.ErrUnavailable) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "guest service unavailable"})
			return true
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": payload})
	return true
}

func handleGuestTelegramWebhook(w http.ResponseWriter, r *http.Request, cfg config.Config, service telegramWebhookService) bool {
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "telegram service unavailable"})
		return true
	}

	accessToken := strings.TrimSpace(r.URL.Query().Get("access_token"))
	expectedToken := fmt.Sprintf("%x", md5.Sum([]byte(strings.TrimSpace(cfg.TelegramBotToken))))
	if accessToken == "" || !strings.EqualFold(accessToken, expectedToken) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "unauthorized"})
		return true
	}

	var payload map[string]any
	if err := readJSONBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	if payload == nil {
		payload = map[string]any{}
	}

	if err := service.HandleWebhook(r.Context(), payload); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": true})
	return true
}

func handleGuestPaymentNotify(w http.ResponseWriter, r *http.Request, service payment.Service) bool {
	if service == nil {
		http.Error(w, "fail", http.StatusInternalServerError)
		return true
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/v1/guest/payment/notify/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		http.Error(w, "fail", http.StatusInternalServerError)
		return true
	}

	rawBody, err := readRequestBody(r)
	if err != nil {
		http.Error(w, "fail", http.StatusInternalServerError)
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		http.Error(w, "fail", http.StatusInternalServerError)
		return true
	}

	result, err := service.Notify(r.Context(), parts[0], parts[1], payment.NotifyRequest{
		Params:  inputs,
		Headers: r.Header.Clone(),
		Body:    rawBody,
	})
	if err != nil {
		http.Error(w, "fail", http.StatusInternalServerError)
		return true
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(result))
	return true
}

func handleServerUniProxyPush(w http.ResponseWriter, r *http.Request, cfg config.Config, service nodeapi.Service) bool {
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "node service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	if err := validateServerPushToken(cfg, inputs); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}

	nodeID, err := strconv.ParseInt(strings.TrimSpace(inputs["node_id"]), 10, 64)
	if err != nil || nodeID <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "node_id is invalid"})
		return true
	}

	nodeType := nodeapi.NormalizeNodeType(inputs["node_type"])
	if nodeType == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "node_type is invalid"})
		return true
	}

	traffic, err := decodeTrafficPayload(r, false)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	if err := service.PushTraffic(r.Context(), nodeapi.TrafficPushRequest{
		NodeID:   nodeID,
		NodeType: nodeType,
		Traffic:  traffic,
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": true})
	return true
}

func handleServerLegacyTrafficSubmit(w http.ResponseWriter, r *http.Request, cfg config.Config, service nodeapi.Service, nodeType string) bool {
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ret": 0, "msg": "node service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ret": 0, "msg": err.Error()})
		return true
	}
	if err := validateServerPushToken(cfg, inputs); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ret": 0, "msg": err.Error()})
		return true
	}

	nodeID, err := strconv.ParseInt(strings.TrimSpace(inputs["node_id"]), 10, 64)
	if err != nil || nodeID <= 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ret": 0, "msg": "node_id is invalid"})
		return true
	}

	traffic, err := decodeTrafficPayload(r, true)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ret": 0, "msg": err.Error()})
		return true
	}

	if err := service.PushTraffic(r.Context(), nodeapi.TrafficPushRequest{
		NodeID:   nodeID,
		NodeType: nodeapi.NormalizeNodeType(nodeType),
		Traffic:  traffic,
	}); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ret": 0, "msg": err.Error()})
		return true
	}

	writeJSON(w, http.StatusOK, map[string]any{"ret": 1, "msg": "ok"})
	return true
}

func validateServerPushToken(cfg config.Config, inputs map[string]string) error {
	token := strings.TrimSpace(inputs["token"])
	if token == "" {
		return errors.New("token is null")
	}
	if strings.TrimSpace(cfg.ServerToken) == "" || token != strings.TrimSpace(cfg.ServerToken) {
		return errors.New("token is error")
	}
	return nil
}

func decodeTrafficPayload(r *http.Request, legacy bool) (map[int64]nodeapi.TrafficUsage, error) {
	rawBody, err := readRequestBody(r)
	if err != nil {
		return nil, err
	}

	var payload any
	trimmed := strings.TrimSpace(string(rawBody))
	if trimmed != "" {
		if err := json.Unmarshal(rawBody, &payload); err != nil {
			inputs, inputErr := readInputs(r)
			if inputErr != nil {
				return nil, fmt.Errorf("decode traffic payload: %w", err)
			}
			data := strings.TrimSpace(inputs["data"])
			if data == "" {
				return nil, fmt.Errorf("decode traffic payload: %w", err)
			}
			if err := json.Unmarshal([]byte(data), &payload); err != nil {
				return nil, fmt.Errorf("decode traffic payload: %w", err)
			}
		}
	} else {
		inputs, err := readInputs(r)
		if err != nil {
			return nil, err
		}
		data := strings.TrimSpace(inputs["data"])
		if data == "" {
			return map[int64]nodeapi.TrafficUsage{}, nil
		}
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			return nil, fmt.Errorf("decode traffic payload: %w", err)
		}
	}

	if payload == nil {
		return map[int64]nodeapi.TrafficUsage{}, nil
	}
	if legacy {
		return decodeLegacyTrafficPayload(payload)
	}
	return decodeUniProxyTrafficPayload(payload)
}

func decodeUniProxyTrafficPayload(payload any) (map[int64]nodeapi.TrafficUsage, error) {
	if wrapper, ok := payload.(map[string]any); ok {
		if data, exists := wrapper["data"]; exists && data != nil {
			payload = data
		}
	}

	entries, ok := payload.(map[string]any)
	if !ok {
		return nil, errors.New("invalid traffic data")
	}

	result := make(map[int64]nodeapi.TrafficUsage, len(entries))
	for rawUserID, value := range entries {
		userID, err := strconv.ParseInt(strings.TrimSpace(rawUserID), 10, 64)
		if err != nil || userID <= 0 {
			continue
		}
		usage, err := decodeTrafficUsageValue(value)
		if err != nil {
			return nil, err
		}
		result[userID] = usage
	}
	return result, nil
}

func decodeLegacyTrafficPayload(payload any) (map[int64]nodeapi.TrafficUsage, error) {
	if wrapper, ok := payload.(map[string]any); ok {
		if data, exists := wrapper["data"]; exists && data != nil {
			payload = data
		} else {
			return decodeUniProxyTrafficPayload(wrapper)
		}
	}

	items, ok := payload.([]any)
	if !ok {
		return nil, errors.New("invalid traffic data")
	}

	result := make(map[int64]nodeapi.TrafficUsage, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("invalid traffic data")
		}
		userID, err := anyToInt64(entry["user_id"])
		if err != nil || userID <= 0 {
			continue
		}
		u, err := anyToInt64(entry["u"])
		if err != nil {
			return nil, err
		}
		d, err := anyToInt64(entry["d"])
		if err != nil {
			return nil, err
		}
		result[userID] = nodeapi.TrafficUsage{U: u, D: d}
	}
	return result, nil
}

func decodeTrafficUsageValue(value any) (nodeapi.TrafficUsage, error) {
	switch typed := value.(type) {
	case []any:
		if len(typed) < 2 {
			return nodeapi.TrafficUsage{}, errors.New("invalid traffic data")
		}
		u, err := anyToInt64(typed[0])
		if err != nil {
			return nodeapi.TrafficUsage{}, err
		}
		d, err := anyToInt64(typed[1])
		if err != nil {
			return nodeapi.TrafficUsage{}, err
		}
		return nodeapi.TrafficUsage{U: u, D: d}, nil
	case map[string]any:
		u, err := anyToInt64(typed["u"])
		if err != nil {
			return nodeapi.TrafficUsage{}, err
		}
		d, err := anyToInt64(typed["d"])
		if err != nil {
			return nodeapi.TrafficUsage{}, err
		}
		return nodeapi.TrafficUsage{U: u, D: d}, nil
	default:
		return nodeapi.TrafficUsage{}, errors.New("invalid traffic data")
	}
}

func anyToInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case nil:
		return 0, nil
	case int:
		return int64(typed), nil
	case int8:
		return int64(typed), nil
	case int16:
		return int64(typed), nil
	case int32:
		return int64(typed), nil
	case int64:
		return typed, nil
	case uint:
		return int64(typed), nil
	case uint8:
		return int64(typed), nil
	case uint16:
		return int64(typed), nil
	case uint32:
		return int64(typed), nil
	case uint64:
		return int64(typed), nil
	case float32:
		return int64(typed), nil
	case float64:
		return int64(typed), nil
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return parsed, nil
		}
		floatParsed, err := typed.Float64()
		if err != nil {
			return 0, fmt.Errorf("invalid integer value: %v", typed)
		}
		return int64(floatParsed), nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, nil
		}
		if parsed, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return parsed, nil
		}
		floatParsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid integer value: %s", trimmed)
		}
		return int64(floatParsed), nil
	default:
		return 0, fmt.Errorf("invalid integer value: %T", value)
	}
}

func handlePassportPV(w http.ResponseWriter, r *http.Request, service passport.Service) bool {
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "passport service unavailable"})
		return true
	}

	inviteCode, err := readInputValue(r, "invite_code")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	if err := service.PV(r.Context(), inviteCode); err != nil {
		return handlePassportError(w, err)
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": true})
	return true
}

func handlePassportSendEmailVerify(w http.ResponseWriter, r *http.Request, service passport.Service) bool {
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "passport service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	req := passport.SendEmailVerifyRequest{
		Email:         inputs["email"],
		RecaptchaData: inputs["recaptcha_data"],
		IP:            requestIP(r),
		UserAgent:     r.UserAgent(),
	}
	if raw, ok := inputs["isforget"]; ok {
		if rawInt, convErr := strconv.Atoi(strings.TrimSpace(raw)); convErr == nil {
			req.IsForget = rawInt
			req.HasIsForget = true
		}
	}

	if err := service.SendEmailVerify(r.Context(), req); err != nil {
		return handlePassportError(w, err)
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": true})
	return true
}

func handlePassportRegister(w http.ResponseWriter, r *http.Request, service passport.Service) bool {
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "passport service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	payload, err := service.Register(r.Context(), passport.RegisterRequest{
		Email:         inputs["email"],
		Password:      inputs["password"],
		InviteCode:    inputs["invite_code"],
		EmailCode:     inputs["email_code"],
		RecaptchaData: inputs["recaptcha_data"],
		IP:            requestIP(r),
		UserAgent:     r.UserAgent(),
	})
	if err != nil {
		return handlePassportError(w, err)
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": payload})
	return true
}

func handlePassportLogin(w http.ResponseWriter, r *http.Request, service passport.Service) bool {
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "passport service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	payload, err := service.Login(r.Context(), passport.LoginRequest{
		Email:     inputs["email"],
		Password:  inputs["password"],
		IP:        requestIP(r),
		UserAgent: r.UserAgent(),
	})
	if err != nil {
		return handlePassportError(w, err)
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": payload})
	return true
}

func handlePassportForget(w http.ResponseWriter, r *http.Request, service passport.Service) bool {
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "passport service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	if err := service.Forget(r.Context(), passport.ForgetRequest{
		Email:     inputs["email"],
		Password:  inputs["password"],
		EmailCode: inputs["email_code"],
	}); err != nil {
		return handlePassportError(w, err)
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": true})
	return true
}

func handlePassportToken2Login(w http.ResponseWriter, r *http.Request, service passport.Service) bool {
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "passport service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	result, err := service.TokenLogin(r.Context(), passport.TokenLoginRequest{
		Token:     inputs["token"],
		Verify:    inputs["verify"],
		Redirect:  inputs["redirect"],
		IP:        requestIP(r),
		UserAgent: r.UserAgent(),
	})
	if err != nil {
		return handlePassportError(w, err)
	}

	if strings.TrimSpace(result.RedirectURL) != "" {
		http.Redirect(w, r, result.RedirectURL, http.StatusFound)
		return true
	}
	if result.AuthData == nil {
		writeJSON(w, http.StatusOK, map[string]any{"data": true})
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result.AuthData})
	return true
}

func handlePassportGetQuickLoginURL(w http.ResponseWriter, r *http.Request, service passport.Service) bool {
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "passport service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	authData := inputs["auth_data"]
	if strings.TrimSpace(authData) == "" {
		authData = strings.TrimSpace(r.Header.Get("authorization"))
	}

	link, err := service.GetQuickLoginURL(r.Context(), passport.QuickLoginRequest{
		AuthData: authData,
		Redirect: inputs["redirect"],
	})
	if err != nil {
		return handlePassportError(w, err)
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": link})
	return true
}

func handlePassportLoginWithMailLink(w http.ResponseWriter, r *http.Request, service passport.Service) bool {
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "passport service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	result, err := service.LoginWithMailLink(r.Context(), passport.LoginWithMailLinkRequest{
		Email:    inputs["email"],
		Redirect: inputs["redirect"],
	})
	if err != nil {
		return handlePassportError(w, err)
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": result})
	return true
}

func handleUserCheckLogin(w http.ResponseWriter, r *http.Request, service session.Service) bool {
	identity, ok := authenticateRequest(w, r, service, false)
	if !ok {
		return true
	}

	data := map[string]any{
		"is_login": true,
	}
	if identity.IsAdmin != 0 {
		data["is_admin"] = true
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleUserInfo(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	info, err := userService.Info(r.Context(), identity.ID)
	if err != nil {
		return handleUserError(w, err)
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": info})
	return true
}

func handleUserGetSubscribe(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	subscribe, err := userService.Subscribe(r.Context(), identity.ID)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": subscribe})
	return true
}

func handleUserUnbindTelegram(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	updated, err := userService.UnbindTelegram(r.Context(), identity.ID)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": updated})
	return true
}

func handleUserResetSecurity(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	data, err := userService.ResetSecurity(r.Context(), identity.ID)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleUserUpdate(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	req := usersvc.ProfileUpdateRequest{}
	if raw := strings.TrimSpace(inputs["auto_renewal"]); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || !containsInt64([]int64{0, 1}, parsed) {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Incorrect auto renewal format"})
			return true
		}
		req.AutoRenewal = &parsed
	}
	if raw := strings.TrimSpace(inputs["remind_expire"]); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || !containsInt64([]int64{0, 1}, parsed) {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Incorrect format of expiration reminder"})
			return true
		}
		req.RemindExpire = &parsed
	}
	if raw := strings.TrimSpace(inputs["remind_traffic"]); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || !containsInt64([]int64{0, 1}, parsed) {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Incorrect traffic alert format"})
			return true
		}
		req.RemindTraffic = &parsed
	}

	updated, err := userService.UpdateProfile(r.Context(), identity.ID, req)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": updated})
	return true
}

func handleUserChangePassword(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	oldPassword := strings.TrimSpace(inputs["old_password"])
	if oldPassword == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Old password cannot be empty"})
		return true
	}
	newPassword := strings.TrimSpace(inputs["new_password"])
	if newPassword == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "New password cannot be empty"})
		return true
	}
	if len([]rune(newPassword)) < 8 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Password must be greater than 8 digits"})
		return true
	}

	updated, err := userService.ChangePassword(r.Context(), identity.ID, oldPassword, newPassword)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": updated})
	return true
}

func handleUserTransfer(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	raw := strings.TrimSpace(inputs["transfer_amount"])
	if raw == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "The transfer amount cannot be empty"})
		return true
	}
	amount, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || amount < 1 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "The transfer amount parameter is wrong"})
		return true
	}

	updated, err := userService.Transfer(r.Context(), identity.ID, amount)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": updated})
	return true
}

func handleUserNewPeriod(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	updated, err := userService.NewPeriod(r.Context(), identity.ID)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": updated})
	return true
}

func handleUserRedeemGiftcard(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	giftcard := strings.TrimSpace(inputs["giftcard"])
	if giftcard == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Giftcard cannot be empty"})
		return true
	}

	result, err := userService.RedeemGiftcard(r.Context(), identity.ID, giftcard)
	if err != nil {
		return handleUserError(w, err)
	}

	response := map[string]any{"data": true}
	for key, value := range result {
		response[key] = value
	}
	writeJSON(w, http.StatusOK, response)
	return true
}

func handleUserServerFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	data, err := userService.Servers(r.Context(), identity.ID, r.UserAgent())
	if err != nil {
		return handleUserError(w, err)
	}
	etag := serverFetchETag(data)
	if strings.Contains(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	w.Header().Set("ETag", `"`+etag+`"`)
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleUserTelegramGetBotInfo(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	_, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	data, err := userService.TelegramBotInfo(r.Context())
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func serverFetchETag(data []map[string]any) string {
	cacheKeys := make([]string, 0, len(data))
	for _, item := range data {
		cacheKeys = append(cacheKeys, strings.TrimSpace(fmt.Sprint(item["cache_key"])))
	}
	raw, _ := json.Marshal(cacheKeys)
	return fmt.Sprintf("%x", sha1.Sum(raw))
}

func handleUserPlanFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	var planID *int64
	if raw := strings.TrimSpace(inputs["id"]); raw != "" {
		parsed, convErr := strconv.ParseInt(raw, 10, 64)
		if convErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": convErr.Error()})
			return true
		}
		planID = &parsed
	}

	payload, err := userService.Plans(r.Context(), identity.ID, planID)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": payload})
	return true
}

func handleUserNoticeFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	_, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	if rawID := strings.TrimSpace(inputs["id"]); rawID != "" {
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "Notice not found"})
			return true
		}
		data, err := userService.NoticeDetail(r.Context(), id)
		if err != nil {
			return handleUserError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
		return true
	}

	current := int64(1)
	if raw := strings.TrimSpace(inputs["current"]); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			current = parsed
		}
	}
	pageSize := int64(5)
	if raw := strings.TrimSpace(inputs["pageSize"]); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			if parsed < 1 {
				parsed = 1
			}
			if parsed > 100 {
				parsed = 100
			}
			pageSize = parsed
		}
	}

	data, total, err := userService.Notices(r.Context(), current, pageSize)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":  data,
		"total": total,
	})
	return true
}

func handleUserInviteSave(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	saved, err := userService.CreateInviteCode(r.Context(), identity.ID)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": saved})
	return true
}

func handleUserInviteFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	data, err := userService.InviteOverview(r.Context(), identity.ID)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleUserInviteDetails(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	current := int64(1)
	if raw := strings.TrimSpace(inputs["current"]); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			current = parsed
		}
	}
	pageSize := int64(10)
	if raw := strings.TrimSpace(inputs["page_size"]); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed >= 10 {
			pageSize = parsed
		}
	}

	data, total, err := userService.InviteDetails(r.Context(), identity.ID, current, pageSize)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":  data,
		"total": total,
	})
	return true
}

func handleUserInviteCampaignSave(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	planID, err := strconv.ParseInt(strings.TrimSpace(inputs["plan_id"]), 10, 64)
	if err != nil || planID <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Invalid parameter"})
		return true
	}
	period := strings.TrimSpace(inputs["period"])
	if period == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "This payment period cannot be purchased, please choose another period"})
		return true
	}

	data, err := userService.CreateInviteCampaign(r.Context(), identity.ID, planID, period)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, data)
	return true
}

func handleUserInviteCampaignFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	data, err := userService.InviteCampaign(r.Context(), identity.ID)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, data)
	return true
}

func handleUserInviteCampaignRecords(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	var campaignID *int64
	if raw := strings.TrimSpace(inputs["campaign_id"]); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Invalid parameter"})
			return true
		}
		campaignID = &parsed
	}
	current := int64(1)
	if raw := strings.TrimSpace(inputs["current"]); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			current = parsed
		}
	}
	pageSize := int64(10)
	if raw := strings.TrimSpace(inputs["page_size"]); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			pageSize = parsed
		}
	}

	data, err := userService.InviteCampaignRecords(r.Context(), identity.ID, campaignID, current, pageSize)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, data)
	return true
}

func handleUserInviteCampaignAbandon(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	updated, err := userService.AbandonInviteCampaign(r.Context(), identity.ID)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": updated})
	return true
}

func handleUserTicketFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	if rawID := strings.TrimSpace(inputs["id"]); rawID != "" {
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Ticket does not exist"})
			return true
		}
		data, err := userService.TicketDetail(r.Context(), identity.ID, id)
		if err != nil {
			return handleUserError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
		return true
	}

	data, err := userService.Tickets(r.Context(), identity.ID)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleUserTicketSave(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	subject := strings.TrimSpace(inputs["subject"])
	if subject == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Ticket subject cannot be empty"})
		return true
	}

	levelRaw := strings.TrimSpace(inputs["level"])
	if levelRaw == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Ticket level cannot be empty"})
		return true
	}
	level, err := strconv.ParseInt(levelRaw, 10, 64)
	if err != nil || level < 0 || level > 2 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Incorrect ticket level format"})
		return true
	}

	message := strings.TrimSpace(inputs["message"])
	if message == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Message cannot be empty"})
		return true
	}

	saved, err := userService.CreateTicket(r.Context(), identity.ID, usersvc.TicketCreateRequest{
		Subject: subject,
		Level:   level,
		Message: message,
	})
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": saved})
	return true
}

func handleUserTicketReply(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Invalid parameter"})
		return true
	}
	message := strings.TrimSpace(inputs["message"])
	if message == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Message cannot be empty"})
		return true
	}

	saved, err := userService.ReplyTicket(r.Context(), identity.ID, id, message)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": saved})
	return true
}

func handleUserTicketClose(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Invalid parameter"})
		return true
	}

	closed, err := userService.CloseTicket(r.Context(), identity.ID, id)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": closed})
	return true
}

func handleUserTicketWithdraw(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	method := strings.TrimSpace(inputs["withdraw_method"])
	if method == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "The withdrawal method cannot be empty"})
		return true
	}
	account := strings.TrimSpace(inputs["withdraw_account"])
	if account == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "The withdrawal account cannot be empty"})
		return true
	}

	saved, err := userService.WithdrawTicket(r.Context(), identity.ID, method, account)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": saved})
	return true
}

func handleUserCommConfig(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	_, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	data, err := userService.CommConfig(r.Context())
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleUserCommGetStripePublicKey(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	_, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	paymentID, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || paymentID <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Invalid parameter"})
		return true
	}

	key, err := userService.StripePublicKey(r.Context(), paymentID)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": key})
	return true
}

func handleUserCouponCheck(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	code := strings.TrimSpace(inputs["code"])
	if code == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Coupon cannot be empty"})
		return true
	}

	var planID *int64
	if raw := strings.TrimSpace(inputs["plan_id"]); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Invalid parameter"})
			return true
		}
		planID = &parsed
	}

	data, err := userService.CheckCoupon(r.Context(), identity.ID, code, planID)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleUserKnowledgeFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	if rawID := strings.TrimSpace(inputs["id"]); rawID != "" {
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || id <= 0 {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Article does not exist"})
			return true
		}
		data, err := userService.KnowledgeDetail(r.Context(), identity.ID, id)
		if err != nil {
			return handleUserError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
		return true
	}

	data, err := userService.Knowledges(r.Context(), strings.TrimSpace(inputs["language"]), strings.TrimSpace(inputs["keyword"]))
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleUserKnowledgeGetCategory(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	_, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	language := strings.TrimSpace(r.URL.Query().Get("language"))
	data, err := userService.KnowledgeCategories(r.Context(), language)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleUserStatGetTrafficLog(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	data, err := userService.TrafficLogs(r.Context(), identity.ID)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleUserGetActiveSession(w http.ResponseWriter, r *http.Request, service session.Service) bool {
	identity, ok := authenticateRequest(w, r, service, false)
	if !ok {
		return true
	}

	sessions, err := service.ListSessions(r.Context(), identity.ID)
	if err != nil {
		return handleSessionError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": sessions})
	return true
}

func handleUserGetStat(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	stats, err := userService.Stat(r.Context(), identity.ID)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": stats})
	return true
}

func handleUserOrderFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	var status *int64
	if raw := strings.TrimSpace(inputs["status"]); raw != "" {
		parsed, convErr := strconv.ParseInt(raw, 10, 64)
		if convErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": convErr.Error()})
			return true
		}
		status = &parsed
	}

	orders, err := userService.Orders(r.Context(), identity.ID, status)
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": orders})
	return true
}

func handleUserOrderSave(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	planID, err := strconv.ParseInt(strings.TrimSpace(inputs["plan_id"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	depositAmount := int64(0)
	if raw := strings.TrimSpace(inputs["deposit_amount"]); raw != "" {
		depositAmount, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
			return true
		}
	}

	tradeNo, err := userService.SaveOrder(r.Context(), identity.ID, usersvc.OrderSaveRequest{
		PlanID:        planID,
		Period:        strings.TrimSpace(inputs["period"]),
		CouponCode:    strings.TrimSpace(inputs["coupon_code"]),
		DepositAmount: depositAmount,
	})
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": tradeNo})
	return true
}

func handleUserOrderDetail(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	detail, err := userService.OrderDetail(r.Context(), identity.ID, inputs["trade_no"])
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": detail})
	return true
}

func handleUserOrderCheckout(w http.ResponseWriter, r *http.Request, sessionService session.Service, paymentService payment.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if paymentService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "payment service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	methodID := int64(0)
	if raw := strings.TrimSpace(inputs["method"]); raw != "" {
		methodID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
			return true
		}
	}

	result, err := paymentService.Checkout(r.Context(), identity.ID, payment.CheckoutRequest{
		TradeNo:        strings.TrimSpace(inputs["trade_no"]),
		MethodID:       methodID,
		Token:          strings.TrimSpace(inputs["token"]),
		RequestBaseURL: detectCheckoutRequestBaseURL(r),
	})
	if err != nil {
		return handlePaymentError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"type": result.Type,
		"data": result.Data,
	})
	return true
}

func detectCheckoutRequestBaseURL(r *http.Request) string {
	if r == nil {
		return ""
	}
	if base := detectPublicBaseURL(r.Header.Get("Origin")); base != "" {
		return base
	}
	if base := detectPublicBaseURL(r.Referer()); base != "" {
		return base
	}
	return ""
}

func detectPublicBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return parsed.Scheme + "://" + parsed.Host
	default:
		return ""
	}
}

func handleUserOrderCheck(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	status, err := userService.OrderStatus(r.Context(), identity.ID, inputs["trade_no"])
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": status})
	return true
}

func handleUserOrderGetPaymentMethod(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, false); !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	methods, err := userService.PaymentMethods(r.Context())
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": methods})
	return true
}

func handleUserOrderCancel(w http.ResponseWriter, r *http.Request, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	okResult, err := userService.CancelOrder(r.Context(), identity.ID, strings.TrimSpace(inputs["trade_no"]))
	if err != nil {
		return handleUserError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": okResult})
	return true
}

func handleUserRemoveActiveSession(w http.ResponseWriter, r *http.Request, service session.Service) bool {
	identity, ok := authenticateRequest(w, r, service, false)
	if !ok {
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	removed, err := service.RemoveSession(r.Context(), identity.ID, inputs["session_id"])
	if err != nil {
		return handleSessionError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": removed})
	return true
}

func handleStaffPlanFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateStaffRequest(w, r, sessionService); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	plans, err := adminService.ListPlans(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": plans})
	return true
}

func handleStaffNoticeFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateStaffRequest(w, r, sessionService); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	notices, err := adminService.ListNotices(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": notices})
	return true
}

func handleStaffNoticeSave(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateStaffRequest(w, r, sessionService); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	var payload struct {
		ID      *json.Number `json:"id"`
		Title   string       `json:"title"`
		Content string       `json:"content"`
		ImgURL  *string      `json:"img_url"`
		Tags    []string     `json:"tags"`
	}
	if err := readJSONBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	if payload.ID == nil {
		payload.ID = jsonNumberFromInput(inputs, "id")
	}
	if strings.TrimSpace(payload.Title) == "" {
		payload.Title = strings.TrimSpace(inputs["title"])
	}
	if strings.TrimSpace(payload.Content) == "" {
		payload.Content = strings.TrimSpace(inputs["content"])
	}
	if payload.ImgURL == nil {
		payload.ImgURL = stringPointerFromInputs(inputs, "img_url")
	}
	if len(payload.Tags) == 0 {
		payload.Tags = indexedStrings(inputs, "tags")
		if len(payload.Tags) == 0 {
			payload.Tags = parseStringList(inputs["tags"])
		}
	}

	id, err := jsonNumberToInt64Pointer(payload.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "保存失败"})
		return true
	}

	saved, err := adminService.SaveNotice(r.Context(), admin.NoticeSaveRequest{
		ID:      id,
		Title:   payload.Title,
		Content: payload.Content,
		ImgURL:  normalizedOptionalString(payload.ImgURL),
		Tags:    payload.Tags,
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": saved})
	return true
}

func handleStaffNoticeDrop(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateStaffRequest(w, r, sessionService); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "公告不存在"})
		return true
	}

	deleted, err := adminService.DeleteNotice(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": deleted})
	return true
}

func handleStaffTicketFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateStaffRequest(w, r, sessionService); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	if rawID := strings.TrimSpace(inputs["id"]); rawID != "" {
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "工单不存在"})
			return true
		}
		ticket, err := adminService.GetTicket(r.Context(), id)
		if err != nil {
			return handleAdminError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": ticket})
		return true
	}

	current := int64(1)
	if raw := strings.TrimSpace(inputs["current"]); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			current = parsed
		}
	}
	pageSize := int64(10)
	if raw := strings.TrimSpace(inputs["pageSize"]); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed >= 10 {
			pageSize = parsed
		}
	}

	status, err := parseOptionalInt64(inputs["status"])
	if err != nil {
		status = nil
	}
	result, err := adminService.ListTickets(r.Context(), admin.TicketListRequest{
		Current:     current,
		PageSize:    pageSize,
		Status:      status,
		ReplyStatus: ticketReplyStatusInputs(inputs),
		Email:       strings.TrimSpace(inputs["email"]),
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":  result.Data,
		"total": result.Total,
	})
	return true
}

func handleStaffTicketReply(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	identity, ok := authenticateStaffRequest(w, r, sessionService)
	if !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "参数错误"})
		return true
	}
	message := strings.TrimSpace(inputs["message"])
	if message == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "消息不能为空"})
		return true
	}

	saved, err := adminService.ReplyTicket(r.Context(), admin.TicketReplyRequest{
		ID:      id,
		Message: message,
		AdminID: identity.ID,
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": saved})
	return true
}

func handleStaffTicketClose(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateStaffRequest(w, r, sessionService); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "参数错误"})
		return true
	}

	closed, err := adminService.CloseTicket(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": closed})
	return true
}

func handleStaffUserGetInfo(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateStaffRequest(w, r, sessionService); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "参数错误"})
		return true
	}

	data, err := adminService.GetUserInfoByID(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	if !isStaffManageableUser(data) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "用户不存在"})
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleStaffUserUpdate(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateStaffRequest(w, r, sessionService); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "用户不存在"})
		return true
	}

	data, err := adminService.GetUserInfoByID(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	if !isStaffManageableUser(data) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "用户不存在"})
		return true
	}

	values := copyInputsWithout(inputs, "auth_data", "id")
	values["is_admin"] = "0"
	values["is_staff"] = "0"
	updated, err := adminService.UpdateUser(r.Context(), admin.UserUpdateRequest{
		ID:     id,
		Values: values,
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": updated})
	return true
}

func handleStaffUserSendMail(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateStaffRequest(w, r, sessionService); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	sent, err := adminService.SendUserMail(r.Context(), admin.UserMailRequest{
		Subject:  strings.TrimSpace(inputs["subject"]),
		Content:  strings.TrimSpace(inputs["content"]),
		Sort:     strings.TrimSpace(inputs["sort"]),
		SortType: strings.TrimSpace(inputs["sort_type"]),
		Filters:  enforceStaffUserScope(userFilters(inputs)),
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": sent})
	return true
}

func handleStaffUserBan(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateStaffRequest(w, r, sessionService); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	banned, err := adminService.BanUsers(r.Context(), enforceStaffUserScope(userFilters(inputs)))
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": banned})
	return true
}

func handleAdminSystemStatus(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	status, err := adminService.GetSystemStatus(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": status})
	return true
}

func handleAdminUserFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	current := int64(1)
	if raw := strings.TrimSpace(inputs["current"]); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			current = parsed
		}
	}
	pageSize := int64(10)
	if raw := strings.TrimSpace(inputs["pageSize"]); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed >= 10 {
			pageSize = parsed
		}
	}

	result, err := adminService.ListUsers(r.Context(), admin.UserFetchRequest{
		Current:  current,
		PageSize: pageSize,
		Sort:     strings.TrimSpace(inputs["sort"]),
		SortType: strings.TrimSpace(inputs["sort_type"]),
		Filters:  userFilters(inputs),
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":  result.Data,
		"total": result.Total,
	})
	return true
}

func handleAdminUserGetInfo(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "参数错误"})
		return true
	}

	data, err := adminService.GetUserInfoByID(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleAdminUserUpdate(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "用户不存在"})
		return true
	}

	updated, err := adminService.UpdateUser(r.Context(), admin.UserUpdateRequest{
		ID:     id,
		Values: copyInputsWithout(inputs, "auth_data", "id"),
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": updated})
	return true
}

func handleAdminUserSetInviteUser(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "用户不存在"})
		return true
	}

	values := map[string]string{}
	if rawInviteUserEmail, ok := inputs["invite_user_email"]; ok {
		values["invite_user_email"] = rawInviteUserEmail
	}

	updated, err := adminService.UpdateUser(r.Context(), admin.UserUpdateRequest{
		ID:     id,
		Values: values,
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": updated})
	return true
}

func handleAdminUserGenerate(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	content, isBatch, err := adminService.GenerateUsers(r.Context(), admin.UserGenerateRequest{
		Values: copyInputsWithout(inputs, "auth_data"),
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	if isBatch {
		writePlainText(w, http.StatusOK, content)
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": true})
	return true
}

func handleAdminUserDumpCSV(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	data, err := adminService.DumpUserCSV(r.Context(), userFilters(inputs))
	if err != nil {
		return handleAdminError(w, err)
	}
	writePlainText(w, http.StatusOK, data)
	return true
}

func handleAdminUserSendMail(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	sent, err := adminService.SendUserMail(r.Context(), admin.UserMailRequest{
		Subject:  strings.TrimSpace(inputs["subject"]),
		Content:  strings.TrimSpace(inputs["content"]),
		Sort:     strings.TrimSpace(inputs["sort"]),
		SortType: strings.TrimSpace(inputs["sort_type"]),
		Filters:  userFilters(inputs),
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": sent})
	return true
}

func handleAdminUserBan(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	banned, err := adminService.BanUsers(r.Context(), userFilters(inputs))
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": banned})
	return true
}

func handleAdminUserResetSecret(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "用户不存在"})
		return true
	}

	updated, err := adminService.ResetUserSecret(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": updated})
	return true
}

func handleAdminUserDelUser(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "用户不存在"})
		return true
	}

	deleted, err := adminService.DeleteUser(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": deleted})
	return true
}

func handleAdminUserAllDel(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	deleted, err := adminService.DeleteUsers(r.Context(), userFilters(inputs))
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": deleted})
	return true
}

func handleAdminServerGroupFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	var groupID *int64
	if raw := strings.TrimSpace(inputs["group_id"]); raw != "" {
		parsedID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsedID <= 0 {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "组不存在"})
			return true
		}
		groupID = &parsedID
	}

	data, err := adminService.ListServerGroups(r.Context(), groupID)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleAdminServerGroupSave(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	var payload struct {
		ID   *json.Number `json:"id"`
		Name string       `json:"name"`
	}
	if err := readJSONBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	if payload.ID == nil {
		payload.ID = jsonNumberFromInput(inputs, "id")
	}
	if strings.TrimSpace(payload.Name) == "" {
		payload.Name = strings.TrimSpace(inputs["name"])
	}

	id, err := jsonNumberToInt64Pointer(payload.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "保存失败"})
		return true
	}

	saved, err := adminService.SaveServerGroup(r.Context(), admin.ServerGroupSaveRequest{
		ID:   id,
		Name: payload.Name,
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": saved})
	return true
}

func handleAdminServerGroupDrop(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "组不存在"})
		return true
	}

	deleted, err := adminService.DeleteServerGroup(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": deleted})
	return true
}

func handleAdminServerRouteFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	data, err := adminService.ListServerRoutes(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleAdminServerRouteSave(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	var payload struct {
		ID          *json.Number `json:"id"`
		Remarks     string       `json:"remarks"`
		Match       []string     `json:"match"`
		Action      string       `json:"action"`
		ActionValue *string      `json:"action_value"`
	}
	if err := readJSONBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	if payload.ID == nil {
		payload.ID = jsonNumberFromInput(inputs, "id")
	}
	if strings.TrimSpace(payload.Remarks) == "" {
		payload.Remarks = strings.TrimSpace(inputs["remarks"])
	}
	if len(payload.Match) == 0 {
		payload.Match = indexedStrings(inputs, "match")
		if len(payload.Match) == 0 {
			payload.Match = parseStringList(inputs["match"])
		}
	}
	if strings.TrimSpace(payload.Action) == "" {
		payload.Action = strings.TrimSpace(inputs["action"])
	}
	if payload.ActionValue == nil {
		payload.ActionValue = stringPointerFromInputs(inputs, "action_value")
	}

	id, err := jsonNumberToInt64Pointer(payload.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "保存失败"})
		return true
	}

	saved, err := adminService.SaveServerRoute(r.Context(), admin.ServerRouteSaveRequest{
		ID:          id,
		Remarks:     payload.Remarks,
		Match:       payload.Match,
		Action:      payload.Action,
		ActionValue: normalizedOptionalString(payload.ActionValue),
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": saved})
	return true
}

func handleAdminServerRouteDrop(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "路由不存在"})
		return true
	}

	deleted, err := adminService.DeleteServerRoute(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": deleted})
	return true
}

func handleAdminServerManageGetNodes(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	data, err := adminService.ListManagedServers(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	groups, err := adminService.ListClientEntryGroups(r.Context(), nil)
	if err != nil {
		return handleAdminError(w, err)
	}
	data = enrichManagedServersWithClientEntryGroups(data, groups)
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleAdminServerManageSort(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	values, err := readManagedServerSortPayload(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	saved, err := adminService.SortManagedServers(r.Context(), values)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": saved})
	return true
}

func handleAdminServerManageUpdateHost(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	var payload struct {
		OldHost string `json:"old_host"`
		NewHost string `json:"new_host"`
	}
	if err := readJSONBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	if strings.TrimSpace(payload.OldHost) == "" {
		payload.OldHost = strings.TrimSpace(inputs["old_host"])
	}
	if strings.TrimSpace(payload.NewHost) == "" {
		payload.NewHost = strings.TrimSpace(inputs["new_host"])
	}

	result, err := adminService.UpdateManagedServerHost(r.Context(), payload.OldHost, payload.NewHost)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
	return true
}

func handleAdminManagedServerAction(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service, path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 {
		return false
	}

	serverType := strings.TrimSpace(parts[0])
	if _, ok := managedServerRouterTypes[serverType]; !ok {
		return false
	}

	switch strings.TrimSpace(parts[1]) {
	case "save":
		return handleAdminManagedServerSave(w, r, sessionService, adminService, serverType)
	case "drop":
		return handleAdminManagedServerDrop(w, r, sessionService, adminService, serverType)
	case "update":
		return handleAdminManagedServerUpdate(w, r, sessionService, adminService, serverType)
	case "copy":
		return handleAdminManagedServerCopy(w, r, sessionService, adminService, serverType)
	default:
		return false
	}
}

func handleAdminManagedServerSave(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service, serverType string) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	payload, err := readManagedServerPayload(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	delete(payload, "auth_data")

	saved, err := adminService.SaveManagedServer(r.Context(), serverType, payload)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": saved})
	return true
}

func handleAdminManagedServerDrop(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service, serverType string) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "节点ID不存在"})
		return true
	}

	deleted, err := adminService.DeleteManagedServer(r.Context(), serverType, id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": deleted})
	return true
}

func handleAdminManagedServerUpdate(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service, serverType string) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	payload, err := readManagedServerPayload(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	id, ok := managedSortAnyToInt64(payload["id"])
	if !ok || id <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "该服务器不存在"})
		return true
	}
	delete(payload, "id")
	delete(payload, "auth_data")

	updated, err := adminService.UpdateManagedServer(r.Context(), serverType, id, payload)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": updated})
	return true
}

func handleAdminManagedServerCopy(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service, serverType string) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "服务器不存在"})
		return true
	}

	copied, err := adminService.CopyManagedServer(r.Context(), serverType, id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": copied})
	return true
}

func handleAdminConfigFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	data, err := adminService.FetchConfig(r.Context(), strings.TrimSpace(inputs["key"]))
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleAdminConfigSave(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	payload, err := readConfigSavePayload(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	saved, err := adminService.SaveConfig(r.Context(), payload)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": saved})
	return true
}

func handleAdminConfigEmailTemplate(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	templates, err := adminService.ListEmailTemplates(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": templates})
	return true
}

func handleAdminConfigSetTelegramWebhook(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	updated, err := adminService.SetTelegramWebhook(r.Context(), strings.TrimSpace(inputs["telegram_bot_token"]))
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": updated})
	return true
}

func handleAdminConfigTestSendMail(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, true)
	if !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	log, err := adminService.TestSendMail(r.Context(), identity.Email)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": true,
		"log":  log,
	})
	return true
}

func handleAdminPlanFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	plans, err := adminService.ListPlans(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": plans})
	return true
}

func handleAdminPlanSave(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	var payload struct {
		ID                 *json.Number `json:"id"`
		Name               string       `json:"name"`
		Content            *string      `json:"content"`
		GroupID            *json.Number `json:"group_id"`
		TransferEnable     *json.Number `json:"transfer_enable"`
		DeviceLimit        *json.Number `json:"device_limit"`
		MonthPrice         *json.Number `json:"month_price"`
		QuarterPrice       *json.Number `json:"quarter_price"`
		HalfYearPrice      *json.Number `json:"half_year_price"`
		YearPrice          *json.Number `json:"year_price"`
		TwoYearPrice       *json.Number `json:"two_year_price"`
		ThreeYearPrice     *json.Number `json:"three_year_price"`
		OnetimePrice       *json.Number `json:"onetime_price"`
		ResetPrice         *json.Number `json:"reset_price"`
		ResetTrafficMethod *json.Number `json:"reset_traffic_method"`
		CapacityLimit      *json.Number `json:"capacity_limit"`
		SpeedLimit         *json.Number `json:"speed_limit"`
		ForceUpdate        *bool        `json:"force_update"`
	}
	if err := readJSONBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	if payload.ID == nil {
		payload.ID = jsonNumberFromInput(inputs, "id")
	}
	if strings.TrimSpace(payload.Name) == "" {
		payload.Name = strings.TrimSpace(inputs["name"])
	}
	if payload.Content == nil {
		payload.Content = stringPointerFromInputs(inputs, "content")
	}
	if payload.GroupID == nil {
		payload.GroupID = jsonNumberFromInput(inputs, "group_id")
	}
	if payload.TransferEnable == nil {
		payload.TransferEnable = jsonNumberFromInput(inputs, "transfer_enable")
	}
	if payload.DeviceLimit == nil {
		payload.DeviceLimit = jsonNumberFromInput(inputs, "device_limit")
	}
	if payload.MonthPrice == nil {
		payload.MonthPrice = jsonNumberFromInput(inputs, "month_price")
	}
	if payload.QuarterPrice == nil {
		payload.QuarterPrice = jsonNumberFromInput(inputs, "quarter_price")
	}
	if payload.HalfYearPrice == nil {
		payload.HalfYearPrice = jsonNumberFromInput(inputs, "half_year_price")
	}
	if payload.YearPrice == nil {
		payload.YearPrice = jsonNumberFromInput(inputs, "year_price")
	}
	if payload.TwoYearPrice == nil {
		payload.TwoYearPrice = jsonNumberFromInput(inputs, "two_year_price")
	}
	if payload.ThreeYearPrice == nil {
		payload.ThreeYearPrice = jsonNumberFromInput(inputs, "three_year_price")
	}
	if payload.OnetimePrice == nil {
		payload.OnetimePrice = jsonNumberFromInput(inputs, "onetime_price")
	}
	if payload.ResetPrice == nil {
		payload.ResetPrice = jsonNumberFromInput(inputs, "reset_price")
	}
	if payload.ResetTrafficMethod == nil {
		payload.ResetTrafficMethod = jsonNumberFromInput(inputs, "reset_traffic_method")
	}
	if payload.CapacityLimit == nil {
		payload.CapacityLimit = jsonNumberFromInput(inputs, "capacity_limit")
	}
	if payload.SpeedLimit == nil {
		payload.SpeedLimit = jsonNumberFromInput(inputs, "speed_limit")
	}
	if payload.ForceUpdate == nil {
		value := parseBoolish(inputs["force_update"])
		payload.ForceUpdate = &value
	}

	if strings.TrimSpace(payload.Name) == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "套餐名称不能为空"})
		return true
	}

	id, err := jsonNumberToInt64Pointer(payload.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "该订阅不存在"})
		return true
	}
	groupID, err := jsonNumberToInt64Pointer(payload.GroupID)
	if err != nil || groupID == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "权限组不能为空"})
		return true
	}
	transferEnable, err := jsonNumberToInt64Pointer(payload.TransferEnable)
	if err != nil || transferEnable == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "流量不能为空"})
		return true
	}
	deviceLimit, err := jsonNumberToInt64Pointer(payload.DeviceLimit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "设备数限制格式有误"})
		return true
	}
	monthPrice, err := jsonNumberToInt64Pointer(payload.MonthPrice)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "月付金额格式有误"})
		return true
	}
	quarterPrice, err := jsonNumberToInt64Pointer(payload.QuarterPrice)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "季付金额格式有误"})
		return true
	}
	halfYearPrice, err := jsonNumberToInt64Pointer(payload.HalfYearPrice)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "半年付金额格式有误"})
		return true
	}
	yearPrice, err := jsonNumberToInt64Pointer(payload.YearPrice)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "年付金额格式有误"})
		return true
	}
	twoYearPrice, err := jsonNumberToInt64Pointer(payload.TwoYearPrice)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "两年付金额格式有误"})
		return true
	}
	threeYearPrice, err := jsonNumberToInt64Pointer(payload.ThreeYearPrice)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "三年付金额格式有误"})
		return true
	}
	onetimePrice, err := jsonNumberToInt64Pointer(payload.OnetimePrice)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "一次性金额有误"})
		return true
	}
	resetPrice, err := jsonNumberToInt64Pointer(payload.ResetPrice)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "流量重置包金额有误"})
		return true
	}
	resetTrafficMethod, err := jsonNumberToInt64Pointer(payload.ResetTrafficMethod)
	if err != nil || (resetTrafficMethod != nil && !containsInt64([]int64{0, 1, 2, 3, 4}, *resetTrafficMethod)) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "流量重置方式格式有误"})
		return true
	}
	capacityLimit, err := jsonNumberToInt64Pointer(payload.CapacityLimit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "容纳用户量限制格式有误"})
		return true
	}
	speedLimit, err := jsonNumberToInt64Pointer(payload.SpeedLimit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "限速格式有误"})
		return true
	}

	saved, err := adminService.SavePlan(r.Context(), admin.PlanSaveRequest{
		ID:                 id,
		Name:               payload.Name,
		Content:            payload.Content,
		GroupID:            *groupID,
		TransferEnable:     *transferEnable,
		DeviceLimit:        deviceLimit,
		MonthPrice:         monthPrice,
		QuarterPrice:       quarterPrice,
		HalfYearPrice:      halfYearPrice,
		YearPrice:          yearPrice,
		TwoYearPrice:       twoYearPrice,
		ThreeYearPrice:     threeYearPrice,
		OnetimePrice:       onetimePrice,
		ResetPrice:         resetPrice,
		ResetTrafficMethod: resetTrafficMethod,
		CapacityLimit:      capacityLimit,
		SpeedLimit:         speedLimit,
		ForceUpdate:        payload.ForceUpdate != nil && *payload.ForceUpdate,
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": saved})
	return true
}

func handleAdminPlanDrop(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "该订阅ID不存在"})
		return true
	}

	dropped, err := adminService.DeletePlan(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": dropped})
	return true
}

func handleAdminPlanUpdate(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	var payload struct {
		ID    *json.Number `json:"id"`
		Show  *json.Number `json:"show"`
		Renew *json.Number `json:"renew"`
	}
	if err := readJSONBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	if payload.ID == nil {
		payload.ID = jsonNumberFromInput(inputs, "id")
	}
	if payload.Show == nil {
		payload.Show = jsonNumberFromInput(inputs, "show")
	}
	if payload.Renew == nil {
		payload.Renew = jsonNumberFromInput(inputs, "renew")
	}

	id, err := jsonNumberToInt64Pointer(payload.ID)
	if err != nil || id == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "该订阅不存在"})
		return true
	}
	show, err := jsonNumberToInt64Pointer(payload.Show)
	if err != nil || (show != nil && !containsInt64([]int64{0, 1}, *show)) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "销售状态格式不正确"})
		return true
	}
	renew, err := jsonNumberToInt64Pointer(payload.Renew)
	if err != nil || (renew != nil && !containsInt64([]int64{0, 1}, *renew)) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "续费状态格式不正确"})
		return true
	}

	updated, err := adminService.TogglePlan(r.Context(), admin.PlanToggleRequest{
		ID:    *id,
		Show:  show,
		Renew: renew,
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": updated})
	return true
}

func handleAdminPlanSort(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	var payload struct {
		PlanIDs []json.Number `json:"plan_ids"`
	}
	if err := readJSONBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	if len(payload.PlanIDs) == 0 {
		inputs, err := readInputs(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
			return true
		}
		payload.PlanIDs = indexedJSONNumbers(inputs, "plan_ids")
	}
	if len(payload.PlanIDs) == 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "订阅计划ID不能为空"})
		return true
	}
	ids, err := jsonNumberSliceToInt64(payload.PlanIDs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "订阅计划ID格式有误"})
		return true
	}

	sorted, err := adminService.SortPlans(r.Context(), ids)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": sorted})
	return true
}

func handleAdminNoticeFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	notices, err := adminService.ListNotices(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": notices})
	return true
}

func handleAdminNoticeSave(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	var payload struct {
		ID      *json.Number `json:"id"`
		Title   string       `json:"title"`
		Content string       `json:"content"`
		ImgURL  *string      `json:"img_url"`
		Tags    []string     `json:"tags"`
	}
	if err := readJSONBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	if payload.ID == nil {
		payload.ID = jsonNumberFromInput(inputs, "id")
	}
	if strings.TrimSpace(payload.Title) == "" {
		payload.Title = strings.TrimSpace(inputs["title"])
	}
	if strings.TrimSpace(payload.Content) == "" {
		payload.Content = strings.TrimSpace(inputs["content"])
	}
	if payload.ImgURL == nil {
		payload.ImgURL = stringPointerFromInputs(inputs, "img_url")
	}
	if len(payload.Tags) == 0 {
		payload.Tags = indexedStrings(inputs, "tags")
		if len(payload.Tags) == 0 {
			payload.Tags = parseStringList(inputs["tags"])
		}
	}

	id, err := jsonNumberToInt64Pointer(payload.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "保存失败"})
		return true
	}
	if strings.TrimSpace(payload.Title) == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "标题不能为空"})
		return true
	}
	if strings.TrimSpace(payload.Content) == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "内容不能为空"})
		return true
	}
	imgURL, err := normalizedURLPointer(payload.ImgURL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "图片URL格式不正确"})
		return true
	}

	saved, err := adminService.SaveNotice(r.Context(), admin.NoticeSaveRequest{
		ID:      id,
		Title:   payload.Title,
		Content: payload.Content,
		ImgURL:  imgURL,
		Tags:    payload.Tags,
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": saved})
	return true
}

func handleAdminNoticeDrop(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "参数错误"})
		return true
	}

	dropped, err := adminService.DeleteNotice(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": dropped})
	return true
}

func handleAdminNoticeShow(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "参数有误"})
		return true
	}

	shown, err := adminService.ToggleNotice(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": shown})
	return true
}

func handleAdminCouponFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	current, _ := strconv.ParseInt(strings.TrimSpace(inputs["current"]), 10, 64)
	pageSize, _ := strconv.ParseInt(strings.TrimSpace(inputs["pageSize"]), 10, 64)
	result, err := adminService.ListCoupons(r.Context(), admin.CouponListRequest{
		Current:  current,
		PageSize: pageSize,
		Sort:     strings.TrimSpace(inputs["sort"]),
		SortType: strings.TrimSpace(inputs["sort_type"]),
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result.Data, "total": result.Total})
	return true
}

func handleAdminCouponGenerate(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	var payload struct {
		LimitPlanIDs []any `json:"limit_plan_ids"`
		LimitPeriod  []any `json:"limit_period"`
	}
	if err := readJSONBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	generateCount, ok, err := requiredOptionalInt64(inputs["generate_count"])
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "生成数量必须为数字"})
		return true
	}
	if ok && generateCount != nil && *generateCount > 500 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "生成数量最大为500个"})
		return true
	}
	name := strings.TrimSpace(inputs["name"])
	if name == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "名称不能为空"})
		return true
	}
	typeValue, err := strconv.ParseInt(strings.TrimSpace(inputs["type"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "类型不能为空"})
		return true
	}
	if !containsInt64([]int64{1, 2}, typeValue) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "类型格式有误"})
		return true
	}
	value, err := strconv.ParseInt(strings.TrimSpace(inputs["value"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "金额或比例不能为空"})
		return true
	}
	startedAt, err := strconv.ParseInt(strings.TrimSpace(inputs["started_at"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "开始时间不能为空"})
		return true
	}
	endedAt, err := strconv.ParseInt(strings.TrimSpace(inputs["ended_at"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "结束时间不能为空"})
		return true
	}
	id, err := parseOptionalInt64(inputs["id"])
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "保存失败"})
		return true
	}
	limitUse, err := parseOptionalInt64(inputs["limit_use"])
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "最大使用次数格式有误"})
		return true
	}
	limitUseWithUser, err := parseOptionalInt64(inputs["limit_use_with_user"])
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "限制用户使用次数格式有误"})
		return true
	}
	limitPlanIDs, err := int64SliceFromValues(payload.LimitPlanIDs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "指定订阅格式有误"})
		return true
	}
	if len(limitPlanIDs) == 0 {
		values, err := jsonNumberSliceToInt64(indexedJSONNumbers(inputs, "limit_plan_ids"))
		if err == nil {
			limitPlanIDs = values
		}
	}
	limitPeriod := stringSliceFromValues(payload.LimitPeriod)
	if len(limitPeriod) == 0 {
		limitPeriod = indexedStrings(inputs, "limit_period")
	}
	if len(limitPeriod) == 0 {
		limitPeriod = parseStringList(inputs["limit_period"])
	}

	csv, batch, err := adminService.GenerateCoupon(r.Context(), admin.CouponGenerateRequest{
		ID:               id,
		GenerateCount:    generateCount,
		Name:             name,
		Type:             typeValue,
		Value:            value,
		StartedAt:        startedAt,
		EndedAt:          endedAt,
		LimitUse:         limitUse,
		LimitUseWithUser: limitUseWithUser,
		LimitPlanIDs:     limitPlanIDs,
		LimitPeriod:      limitPeriod,
		Code:             normalizedOptionalString(stringPointerFromInputs(inputs, "code")),
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	if batch {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(csv))
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": true})
	return true
}

func handleAdminCouponDrop(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "参数有误"})
		return true
	}
	deleted, err := adminService.DeleteCoupon(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": deleted})
	return true
}

func handleAdminCouponShow(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "参数有误"})
		return true
	}
	shown, err := adminService.ToggleCoupon(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": shown})
	return true
}

func handleAdminGiftcardFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	current, _ := strconv.ParseInt(strings.TrimSpace(inputs["current"]), 10, 64)
	pageSize, _ := strconv.ParseInt(strings.TrimSpace(inputs["pageSize"]), 10, 64)
	result, err := adminService.ListGiftcards(r.Context(), admin.GiftcardListRequest{
		Current:  current,
		PageSize: pageSize,
		Sort:     strings.TrimSpace(inputs["sort"]),
		SortType: strings.TrimSpace(inputs["sort_type"]),
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result.Data, "total": result.Total})
	return true
}

func handleAdminGiftcardGenerate(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	generateCount, ok, err := requiredOptionalInt64(inputs["generate_count"])
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "生成数量必须为数字"})
		return true
	}
	if ok && generateCount != nil && *generateCount > 500 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "生成数量最大为500个"})
		return true
	}
	name := strings.TrimSpace(inputs["name"])
	if name == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "名称不能为空"})
		return true
	}
	typeValue, err := strconv.ParseInt(strings.TrimSpace(inputs["type"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "类型不能为空"})
		return true
	}
	if !containsInt64([]int64{1, 2, 3, 4, 5}, typeValue) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "类型格式有误"})
		return true
	}
	var value *int64
	if typeValue != 4 {
		value, err = parseOptionalInt64(inputs["value"])
		if err != nil || value == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "数值不能为空"})
			return true
		}
	}
	planID, err := parseOptionalInt64(inputs["plan_id"])
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "订阅不能为空"})
		return true
	}
	if typeValue == 5 && planID == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "订阅不能为空"})
		return true
	}
	startedAt, err := strconv.ParseInt(strings.TrimSpace(inputs["started_at"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "开始时间不能为空"})
		return true
	}
	endedAt, err := strconv.ParseInt(strings.TrimSpace(inputs["ended_at"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "结束时间不能为空"})
		return true
	}
	id, err := parseOptionalInt64(inputs["id"])
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "礼品卡保存失败"})
		return true
	}
	limitUse, err := parseOptionalInt64(inputs["limit_use"])
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "最大使用次数格式有误"})
		return true
	}

	csv, batch, err := adminService.GenerateGiftcard(r.Context(), admin.GiftcardGenerateRequest{
		ID:            id,
		GenerateCount: generateCount,
		Name:          name,
		Type:          typeValue,
		Value:         value,
		PlanID:        planID,
		StartedAt:     startedAt,
		EndedAt:       endedAt,
		LimitUse:      limitUse,
		Code:          normalizedOptionalString(stringPointerFromInputs(inputs, "code")),
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	if batch {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(csv))
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": true})
	return true
}

func handleAdminGiftcardDrop(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "未找到礼品卡"})
		return true
	}
	deleted, err := adminService.DeleteGiftcard(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": deleted})
	return true
}

func handleAdminKnowledgeFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	if raw := strings.TrimSpace(inputs["id"]); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "知识不存在"})
			return true
		}
		knowledge, err := adminService.GetKnowledge(r.Context(), id)
		if err != nil {
			return handleAdminError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": knowledge})
		return true
	}
	knowledges, err := adminService.ListKnowledges(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": knowledges})
	return true
}

func handleAdminKnowledgeGetCategory(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	categories, err := adminService.ListKnowledgeCategories(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": categories})
	return true
}

func handleAdminKnowledgeSave(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := parseOptionalInt64(inputs["id"])
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "保存失败"})
		return true
	}
	title := strings.TrimSpace(inputs["title"])
	if title == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "标题不能为空"})
		return true
	}
	category := strings.TrimSpace(inputs["category"])
	if category == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "分类不能为空"})
		return true
	}
	language := strings.TrimSpace(inputs["language"])
	if language == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "语言不能为空"})
		return true
	}
	body := strings.TrimSpace(inputs["body"])
	if body == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "内容不能为空"})
		return true
	}
	saved, err := adminService.SaveKnowledge(r.Context(), admin.KnowledgeSaveRequest{
		ID:       id,
		Language: language,
		Category: category,
		Title:    title,
		Body:     body,
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": saved})
	return true
}

func handleAdminKnowledgeShow(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "参数有误"})
		return true
	}
	shown, err := adminService.ToggleKnowledge(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": shown})
	return true
}

func handleAdminKnowledgeDrop(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "参数有误"})
		return true
	}
	deleted, err := adminService.DeleteKnowledge(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": deleted})
	return true
}

func handleAdminKnowledgeSort(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	var payload struct {
		KnowledgeIDs []json.Number `json:"knowledge_ids"`
	}
	if err := readJSONBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	if len(payload.KnowledgeIDs) == 0 {
		inputs, err := readInputs(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
			return true
		}
		payload.KnowledgeIDs = indexedJSONNumbers(inputs, "knowledge_ids")
	}
	if len(payload.KnowledgeIDs) == 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "知识ID不能为空"})
		return true
	}
	ids, err := jsonNumberSliceToInt64(payload.KnowledgeIDs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "知识ID格式有误"})
		return true
	}
	sorted, err := adminService.SortKnowledges(r.Context(), ids)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": sorted})
	return true
}

func handleAdminTicketFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	if rawID := strings.TrimSpace(inputs["id"]); rawID != "" {
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "工单不存在"})
			return true
		}
		ticket, err := adminService.GetTicket(r.Context(), id)
		if err != nil {
			return handleAdminError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": ticket})
		return true
	}

	current := int64(1)
	if raw := strings.TrimSpace(inputs["current"]); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			current = parsed
		}
	}
	pageSize := int64(10)
	if raw := strings.TrimSpace(inputs["pageSize"]); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed >= 10 {
			pageSize = parsed
		}
	}

	status, err := parseOptionalInt64(inputs["status"])
	if err != nil {
		status = nil
	}

	result, err := adminService.ListTickets(r.Context(), admin.TicketListRequest{
		Current:     current,
		PageSize:    pageSize,
		Status:      status,
		ReplyStatus: ticketReplyStatusInputs(inputs),
		Email:       strings.TrimSpace(inputs["email"]),
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":  result.Data,
		"total": result.Total,
	})
	return true
}

func handleAdminTicketReply(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, true)
	if !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "参数错误"})
		return true
	}
	message := strings.TrimSpace(inputs["message"])
	if message == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "消息不能为空"})
		return true
	}

	saved, err := adminService.ReplyTicket(r.Context(), admin.TicketReplyRequest{
		ID:      id,
		Message: message,
		AdminID: identity.ID,
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": saved})
	return true
}

func handleAdminTicketClose(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "参数错误"})
		return true
	}

	closed, err := adminService.CloseTicket(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": closed})
	return true
}

func handleAdminOrderFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	current := int64(1)
	if raw := strings.TrimSpace(inputs["current"]); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			current = parsed
		}
	}
	pageSize := int64(10)
	if raw := strings.TrimSpace(inputs["pageSize"]); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			pageSize = parsed
		}
	}

	result, err := adminService.FetchOrders(r.Context(), admin.OrderFetchRequest{
		Current:      current,
		PageSize:     pageSize,
		IsCommission: parseBoolish(inputs["is_commission"]),
		Filters:      indexedOrderFilters(inputs, "filter"),
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":  result.Data,
		"total": result.Total,
	})
	return true
}

func handleAdminOrderDetail(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "订单不存在"})
		return true
	}

	detail, err := adminService.GetOrderDetail(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": detail})
	return true
}

func handleAdminOrderUpdate(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	commissionStatus, err := parseOptionalInt64(inputs["commission_status"])
	if err != nil || commissionStatus == nil || !containsInt64([]int64{0, 1, 3}, *commissionStatus) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "佣金状态格式不正确"})
		return true
	}

	updated, err := adminService.UpdateOrder(r.Context(), admin.OrderUpdateRequest{
		TradeNo:          strings.TrimSpace(inputs["trade_no"]),
		CommissionStatus: commissionStatus,
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": updated})
	return true
}

func handleAdminOrderPaid(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	updated, err := adminService.MarkOrderPaid(r.Context(), strings.TrimSpace(inputs["trade_no"]))
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": updated})
	return true
}

func handleAdminOrderCancel(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	cancelled, err := adminService.CancelManagedOrder(r.Context(), strings.TrimSpace(inputs["trade_no"]))
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": cancelled})
	return true
}

func handleAdminOrderRefund(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	refunded, err := adminService.RefundManagedOrder(r.Context(), strings.TrimSpace(inputs["trade_no"]))
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": refunded})
	return true
}

func handleAdminOrderAssign(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	email := strings.TrimSpace(inputs["email"])
	if email == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "邮箱不能为空"})
		return true
	}
	planID, err := strconv.ParseInt(strings.TrimSpace(inputs["plan_id"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "订阅不能为空"})
		return true
	}
	totalAmount, err := strconv.ParseInt(strings.TrimSpace(inputs["total_amount"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "支付金额不能为空"})
		return true
	}
	period := strings.TrimSpace(inputs["period"])
	if period == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "订阅周期不能为空"})
		return true
	}
	if !containsString(allowedAdminOrderPeriods, period) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "订阅周期格式有误"})
		return true
	}

	tradeNo, err := adminService.AssignOrder(r.Context(), admin.OrderAssignRequest{
		PlanID:      planID,
		Email:       email,
		TotalAmount: totalAmount,
		Period:      period,
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": tradeNo})
	return true
}

func handleAdminPaymentFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	payments, err := adminService.ListPayments(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": payments})
	return true
}

func handleAdminPaymentMethods(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	methods, err := adminService.ListPaymentMethods(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": methods})
	return true
}

func handleAdminPaymentForm(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	var id *int64
	if rawID := strings.TrimSpace(inputs["id"]); rawID != "" {
		parsedID, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "支付方式不存在"})
			return true
		}
		id = &parsedID
	}

	form, err := adminService.GetPaymentForm(r.Context(), strings.TrimSpace(inputs["payment"]), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": form})
	return true
}

func handleAdminPaymentSave(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	var payload struct {
		ID                 *json.Number   `json:"id"`
		Name               string         `json:"name"`
		Icon               *string        `json:"icon"`
		Payment            string         `json:"payment"`
		Config             map[string]any `json:"config"`
		NotifyDomain       *string        `json:"notify_domain"`
		HandlingFeeFixed   *json.Number   `json:"handling_fee_fixed"`
		HandlingFeePercent *json.Number   `json:"handling_fee_percent"`
	}
	if err := readJSONBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	if payload.ID == nil {
		payload.ID = jsonNumberFromInput(inputs, "id")
	}
	if strings.TrimSpace(payload.Name) == "" {
		payload.Name = strings.TrimSpace(inputs["name"])
	}
	if payload.Icon == nil {
		payload.Icon = stringPointerFromInputs(inputs, "icon")
	}
	if strings.TrimSpace(payload.Payment) == "" {
		payload.Payment = strings.TrimSpace(inputs["payment"])
	}
	if payload.NotifyDomain == nil {
		payload.NotifyDomain = stringPointerFromInputs(inputs, "notify_domain")
	}
	if payload.HandlingFeeFixed == nil {
		payload.HandlingFeeFixed = jsonNumberFromInput(inputs, "handling_fee_fixed")
	}
	if payload.HandlingFeePercent == nil {
		payload.HandlingFeePercent = jsonNumberFromInput(inputs, "handling_fee_percent")
	}
	if len(payload.Config) == 0 {
		payload.Config = map[string]any{}
		for key, value := range inputs {
			if nestedKey, ok := bracketFieldKey(key, "config"); ok {
				payload.Config[nestedKey] = value
			}
		}
	}

	id, err := jsonNumberToInt64Pointer(payload.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "支付方式不存在"})
		return true
	}
	handlingFeeFixed, err := jsonNumberToInt64Pointer(payload.HandlingFeeFixed)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "固定手续费格式有误"})
		return true
	}
	handlingFeePercent, err := jsonNumberToFloat64Pointer(payload.HandlingFeePercent)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "百分比手续费范围须在0.1-100之间"})
		return true
	}
	if handlingFeePercent != nil && (*handlingFeePercent < 0.1 || *handlingFeePercent > 100) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "百分比手续费范围须在0.1-100之间"})
		return true
	}

	notifyDomain, err := normalizedURLPointer(payload.NotifyDomain)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "自定义通知域名格式有误"})
		return true
	}

	saved, err := adminService.SavePayment(r.Context(), admin.PaymentSaveRequest{
		ID:                 id,
		Name:               payload.Name,
		Icon:               normalizedOptionalString(payload.Icon),
		Payment:            payload.Payment,
		Config:             stringMapFromAny(payload.Config),
		NotifyDomain:       notifyDomain,
		HandlingFeeFixed:   handlingFeeFixed,
		HandlingFeePercent: handlingFeePercent,
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": saved})
	return true
}

func handleAdminPaymentDrop(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "支付方式不存在"})
		return true
	}

	dropped, err := adminService.DeletePayment(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": dropped})
	return true
}

func handleAdminPaymentShow(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "支付方式不存在"})
		return true
	}

	updated, err := adminService.TogglePayment(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": updated})
	return true
}

func handleAdminPaymentSort(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	var payload struct {
		IDs []json.Number `json:"ids"`
	}
	if err := readJSONBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	if len(payload.IDs) == 0 {
		inputs, err := readInputs(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
			return true
		}
		payload.IDs = indexedJSONNumbers(inputs, "ids")
	}
	ids, err := jsonNumberSliceToInt64(payload.IDs)
	if err != nil || len(ids) == 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "参数有误"})
		return true
	}

	sorted, err := adminService.SortPayments(r.Context(), ids)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": sorted})
	return true
}

func handleAdminQueueStats(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	stats, err := adminService.GetQueueStats(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": stats})
	return true
}

func handleAdminQueueWorkload(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	workload, err := adminService.GetQueueWorkload(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": workload})
	return true
}

func handleAdminSystemLog(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	current := int64(1)
	if raw := strings.TrimSpace(inputs["current"]); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			current = parsed
		}
	}
	pageSize := int64(10)
	if raw := strings.TrimSpace(inputs["page_size"]); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed >= 10 {
			pageSize = parsed
		}
	}

	data, total, err := adminService.ListSystemLogs(r.Context(), current, pageSize, strings.TrimSpace(inputs["level"]))
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":  data,
		"total": total,
	})
	return true
}

func handleAdminStatOverride(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	data, err := adminService.GetStatOverride(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleAdminStatGetStat(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	startAt, endAt, ok := readLegacyStatRange(w, r)
	if !ok {
		return true
	}

	data, err := adminService.GetStat(r.Context(), startAt, endAt)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleAdminStatOrder(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	data, err := adminService.GetStatOrder(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleAdminStatGetRanking(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	rankingType := strings.TrimSpace(inputs["type"])
	if rankingType == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "参数有误"})
		return true
	}
	startAt, endAt, ok := legacyStatRangeFromInputs(inputs)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "参数有误"})
		return true
	}
	limit := int64(20)
	if raw := strings.TrimSpace(inputs["limit"]); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "参数有误"})
			return true
		}
		limit = parsed
	}

	data, err := adminService.GetRanking(r.Context(), rankingType, startAt, endAt, limit)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleAdminStatGetStatRecord(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	statType := strings.TrimSpace(inputs["type"])
	if statType == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "参数有误"})
		return true
	}
	startAt, endAt, ok := legacyStatRangeFromInputs(inputs)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "参数有误"})
		return true
	}

	data, err := adminService.GetStatRecord(r.Context(), statType, startAt, endAt)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleAdminStatServerLastRank(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	data, err := adminService.GetServerLastRank(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleAdminStatServerTodayRank(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	data, err := adminService.GetServerTodayRank(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleAdminStatInviteLastRank(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	data, err := adminService.GetInviteLastRank(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleAdminStatInviteTodayRank(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	data, err := adminService.GetInviteTodayRank(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleAdminStatUserLastRank(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	data, err := adminService.GetUserLastRank(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleAdminStatUserTodayRank(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	data, err := adminService.GetUserTodayRank(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleAdminStatUser(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	userID, err := strconv.ParseInt(strings.TrimSpace(inputs["user_id"]), 10, 64)
	if err != nil || userID <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "参数有误"})
		return true
	}

	current := int64(1)
	if raw := strings.TrimSpace(inputs["current"]); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			current = parsed
		}
	}
	pageSize := int64(10)
	if raw := strings.TrimSpace(inputs["pageSize"]); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed >= 10 {
			pageSize = parsed
		}
	}

	data, total, err := adminService.GetStatUser(r.Context(), userID, current, pageSize)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":  data,
		"total": total,
	})
	return true
}

func handleAdminInviteCampaignFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	current := int64(1)
	if raw := strings.TrimSpace(inputs["current"]); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			current = parsed
		}
	}
	pageSize := int64(10)
	if raw := strings.TrimSpace(inputs["pageSize"]); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed >= 10 {
			pageSize = parsed
		}
	}

	result, err := adminService.ListInviteCampaigns(r.Context(), admin.InviteCampaignListRequest{
		Current:  current,
		PageSize: pageSize,
		Filters:  inviteCampaignFilters(inputs),
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":  result.Data,
		"total": result.Total,
	})
	return true
}

func handleAdminInviteCampaignDetail(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "任务不存在"})
		return true
	}

	data, err := adminService.GetInviteCampaign(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleAdminInviteCampaignRecords(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	campaignID, err := strconv.ParseInt(strings.TrimSpace(inputs["campaign_id"]), 10, 64)
	if err != nil || campaignID <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "任务不存在"})
		return true
	}

	current := int64(1)
	if raw := strings.TrimSpace(inputs["current"]); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			current = parsed
		}
	}
	pageSize := int64(10)
	if raw := strings.TrimSpace(inputs["page_size"]); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed >= 1 {
			pageSize = parsed
		}
	}

	result, err := adminService.ListInviteCampaignRecords(r.Context(), admin.InviteCampaignRecordListRequest{
		CampaignID: campaignID,
		Current:    current,
		PageSize:   pageSize,
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":  result.Data,
		"total": result.Total,
	})
	return true
}

func handlePassportError(w http.ResponseWriter, err error) bool {
	if errors.Is(err, passport.ErrUnavailable) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "passport service unavailable"})
		return true
	}
	httpErr, ok := err.(*passport.HTTPError)
	if ok {
		writeJSON(w, httpErr.Status, map[string]any{"message": httpErr.Message})
		return true
	}

	writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
	return true
}

func authenticateRequest(w http.ResponseWriter, r *http.Request, service session.Service, requireAdmin bool) (*session.Identity, bool) {
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "session service unavailable"})
		return nil, false
	}

	authToken, err := readAuthToken(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return nil, false
	}

	identity, err := service.Authenticate(r.Context(), authToken, requireAdmin)
	if err != nil {
		handleSessionError(w, err)
		return nil, false
	}
	return identity, true
}

func authenticateStaffRequest(w http.ResponseWriter, r *http.Request, service session.Service) (*session.Identity, bool) {
	identity, ok := authenticateRequest(w, r, service, false)
	if !ok {
		return nil, false
	}
	if identity == nil || identity.IsStaff == 0 {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "未登录或登陆已过期"})
		return nil, false
	}
	return identity, true
}

func handleSessionError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, session.ErrUnauthorized):
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "未登录或登陆已过期"})
		return true
	case errors.Is(err, session.ErrUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "session service unavailable"})
		return true
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}
}

func handleUserError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, usersvc.ErrNoticeNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Notice not found"})
		return true
	case errors.Is(err, usersvc.ErrNotFound):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "The user does not exist"})
		return true
	case errors.Is(err, usersvc.ErrInviteLimitReached):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "The maximum number of creations has been reached"})
		return true
	case errors.Is(err, usersvc.ErrPlanNotFound):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Subscription plan does not exist"})
		return true
	case errors.Is(err, usersvc.ErrOrderPaidOrMissing):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Order does not exist or has been paid"})
		return true
	case errors.Is(err, usersvc.ErrOrderNotFound):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Order does not exist"})
		return true
	case errors.Is(err, usersvc.ErrInvalidParameter):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Invalid parameter"})
		return true
	case errors.Is(err, usersvc.ErrPendingOrderExists):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "You have an unpaid or pending order, please try again later or cancel it"})
		return true
	case errors.Is(err, usersvc.ErrDepositAmountInvalid):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Failed to create order, deposit amount must be greater than 0"})
		return true
	case errors.Is(err, usersvc.ErrDepositAmountTooLarge):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Deposit amount too large, please contact the administrator"})
		return true
	case errors.Is(err, usersvc.ErrPlanSoldOut):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Current product is sold out"})
		return true
	case errors.Is(err, usersvc.ErrPeriodUnavailable):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "This payment period cannot be purchased, please choose another period"})
		return true
	case errors.Is(err, usersvc.ErrResetUnavailable):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Subscription has expired or no active subscription, unable to purchase Data Reset Package"})
		return true
	case errors.Is(err, usersvc.ErrSubscriptionSoldOut):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "This subscription has been sold out, please choose another subscription"})
		return true
	case errors.Is(err, usersvc.ErrPlanCannotRenew):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "This subscription cannot be renewed, please change to another subscription"})
		return true
	case errors.Is(err, usersvc.ErrPlanChangeDisabled):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "目前不允许更改订阅，请联系客服或提交工单操作"})
		return true
	case errors.Is(err, usersvc.ErrPlanExpiredChangeRequired):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "This subscription has expired, please change to another subscription"})
		return true
	case errors.Is(err, usersvc.ErrCouponInvalid):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Invalid coupon"})
		return true
	case errors.Is(err, usersvc.ErrCouponUnavailable):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "This coupon is no longer available"})
		return true
	case errors.Is(err, usersvc.ErrCouponNotStarted):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "This coupon has not yet started"})
		return true
	case errors.Is(err, usersvc.ErrCouponExpired):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "This coupon has expired"})
		return true
	case errors.Is(err, usersvc.ErrCouponPlanRestricted):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "The coupon code cannot be used for this subscription"})
		return true
	case errors.Is(err, usersvc.ErrCouponPeriodRestricted):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "The coupon code cannot be used for this period"})
		return true
	case errors.Is(err, usersvc.ErrCouponUserLimit):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "The coupon can only be used for the allowed number of times per person"})
		return true
	case errors.Is(err, usersvc.ErrCouponFailed):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Coupon failed"})
		return true
	case errors.Is(err, usersvc.ErrInsufficientBalance):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Insufficient balance"})
		return true
	case errors.Is(err, usersvc.ErrCreateOrderFailed):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Failed to create order"})
		return true
	case errors.Is(err, usersvc.ErrPaymentMethodUnavailable):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Payment method is not available"})
		return true
	case errors.Is(err, usersvc.ErrCheckoutFailed):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Request failed, please try again later"})
		return true
	case errors.Is(err, usersvc.ErrCancelPendingOnly):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "You can only cancel pending orders"})
		return true
	case errors.Is(err, usersvc.ErrUnsupportedPaymentGateway):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Payment gateway is unsupported"})
		return true
	case errors.Is(err, usersvc.ErrUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}
}

func handlePaymentError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, payment.ErrInvalidParameter):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Invalid parameter"})
		return true
	case errors.Is(err, payment.ErrOrderPaidOrMissing):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Order does not exist or has been paid"})
		return true
	case errors.Is(err, payment.ErrPaymentMethodUnavailable):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Payment method is not available"})
		return true
	case errors.Is(err, payment.ErrUnsupportedGateway):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Payment gateway is unsupported"})
		return true
	case errors.Is(err, payment.ErrRequestFailed):
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Request failed, please try again later"})
		return true
	case errors.Is(err, payment.ErrUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "payment service unavailable"})
		return true
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}
}

func handleAdminError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, admin.ErrUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}
}

func requestIP(r *http.Request) string {
	for _, header := range []string{"X-Forwarded-For", "X-Real-IP"} {
		value := strings.TrimSpace(r.Header.Get(header))
		if value == "" {
			continue
		}
		first := strings.TrimSpace(strings.Split(value, ",")[0])
		if first != "" {
			return first
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func jsonNumberToInt64Pointer(value *json.Number) (*int64, error) {
	if value == nil {
		return nil, nil
	}
	if next, err := value.Int64(); err == nil {
		return &next, nil
	}
	floatValue, err := value.Float64()
	if err != nil {
		return nil, err
	}
	next := int64(floatValue)
	if float64(next) != floatValue {
		return nil, errors.New("invalid integer")
	}
	return &next, nil
}

func jsonNumberToFloat64Pointer(value *json.Number) (*float64, error) {
	if value == nil {
		return nil, nil
	}
	next, err := value.Float64()
	if err != nil {
		return nil, err
	}
	return &next, nil
}

func jsonNumberSliceToInt64(values []json.Number) ([]int64, error) {
	result := make([]int64, 0, len(values))
	for _, value := range values {
		next, err := jsonNumberToInt64Pointer(&value)
		if err != nil || next == nil {
			return nil, errors.New("invalid integer slice")
		}
		result = append(result, *next)
	}
	return result, nil
}

func normalizedOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func normalizedURLPointer(value *string) (*string, error) {
	trimmed := normalizedOptionalString(value)
	if trimmed == nil {
		return nil, nil
	}
	parsed, err := url.ParseRequestURI(*trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("invalid url")
	}
	return trimmed, nil
}

func stringMapFromAny(values map[string]any) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		if value == nil {
			continue
		}
		result[key] = strings.TrimSpace(fmt.Sprint(value))
	}
	return result
}

func jsonNumberFromInput(inputs map[string]string, key string) *json.Number {
	raw := strings.TrimSpace(inputs[key])
	if raw == "" {
		return nil
	}
	value := json.Number(raw)
	return &value
}

func stringPointerFromInputs(inputs map[string]string, key string) *string {
	value, ok := inputs[key]
	if !ok {
		return nil
	}
	next := value
	return &next
}

func bracketFieldKey(key, prefix string) (string, bool) {
	pattern := prefix + "["
	if !strings.HasPrefix(key, pattern) || !strings.HasSuffix(key, "]") {
		return "", false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(key, pattern), "]")
	if strings.TrimSpace(name) == "" {
		return "", false
	}
	return name, true
}

func indexedJSONNumbers(inputs map[string]string, prefix string) []json.Number {
	type indexedValue struct {
		index int
		value json.Number
	}

	values := make([]indexedValue, 0)
	for key, raw := range inputs {
		name, ok := bracketFieldKey(key, prefix)
		if !ok {
			continue
		}
		index, err := strconv.Atoi(name)
		if err != nil {
			continue
		}
		values = append(values, indexedValue{
			index: index,
			value: json.Number(strings.TrimSpace(raw)),
		})
	}
	slices.SortFunc(values, func(left, right indexedValue) int {
		return left.index - right.index
	})

	result := make([]json.Number, 0, len(values))
	for _, entry := range values {
		result = append(result, entry.value)
	}
	return result
}

func indexedStrings(inputs map[string]string, prefix string) []string {
	type indexedValue struct {
		index int
		value string
	}

	values := make([]indexedValue, 0)
	for key, raw := range inputs {
		name, ok := bracketFieldKey(key, prefix)
		if !ok {
			continue
		}
		index, err := strconv.Atoi(name)
		if err != nil {
			continue
		}
		values = append(values, indexedValue{
			index: index,
			value: strings.TrimSpace(raw),
		})
	}
	slices.SortFunc(values, func(left, right indexedValue) int {
		return left.index - right.index
	})

	result := make([]string, 0, len(values))
	for _, entry := range values {
		if entry.value != "" {
			result = append(result, entry.value)
		}
	}
	return result
}

func inviteCampaignFilters(inputs map[string]string) []admin.InviteCampaignFilter {
	type indexedFilter struct {
		index int
		item  admin.InviteCampaignFilter
	}

	filters := make(map[int]*admin.InviteCampaignFilter)
	for key, value := range inputs {
		index, field, ok := inviteCampaignFilterKey(key)
		if !ok {
			continue
		}
		if _, exists := filters[index]; !exists {
			filters[index] = &admin.InviteCampaignFilter{}
		}
		switch field {
		case "key":
			filters[index].Key = strings.TrimSpace(value)
		case "condition":
			filters[index].Condition = strings.TrimSpace(value)
		case "value":
			filters[index].Value = strings.TrimSpace(value)
		}
	}

	ordered := make([]indexedFilter, 0, len(filters))
	for index, filter := range filters {
		if strings.TrimSpace(filter.Key) == "" {
			continue
		}
		ordered = append(ordered, indexedFilter{index: index, item: *filter})
	}
	slices.SortFunc(ordered, func(left, right indexedFilter) int {
		return left.index - right.index
	})

	result := make([]admin.InviteCampaignFilter, 0, len(ordered))
	for _, item := range ordered {
		result = append(result, item.item)
	}
	return result
}

func inviteCampaignFilterKey(key string) (int, string, bool) {
	if !strings.HasPrefix(key, "filter[") || !strings.HasSuffix(key, "]") {
		return 0, "", false
	}
	rest := strings.TrimPrefix(key, "filter[")
	splitAt := strings.Index(rest, "][")
	if splitAt <= 0 {
		return 0, "", false
	}
	indexRaw := rest[:splitAt]
	field := strings.TrimSuffix(rest[splitAt+2:], "]")
	index, err := strconv.Atoi(indexRaw)
	if err != nil || field == "" {
		return 0, "", false
	}
	return index, field, true
}

func readConfigSavePayload(r *http.Request) (map[string]any, error) {
	payload := make(map[string]any)
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		if err := readJSONBody(r, &payload); err != nil {
			return nil, err
		}
	}

	inputs, err := readInputs(r)
	if err != nil {
		return nil, err
	}

	grouped := make(map[string][]string)
	for key, value := range inputs {
		name, index, ok := indexedConfigFieldKey(key)
		if !ok {
			continue
		}
		for len(grouped[name]) <= index {
			grouped[name] = append(grouped[name], "")
		}
		grouped[name][index] = value
	}

	for key, values := range grouped {
		if _, exists := payload[key]; exists {
			continue
		}
		items := make([]any, 0, len(values))
		for _, value := range values {
			items = append(items, value)
		}
		payload[key] = items
	}

	for key, value := range inputs {
		if key == "auth_data" {
			continue
		}
		if _, _, ok := indexedConfigFieldKey(key); ok {
			continue
		}
		if _, exists := payload[key]; exists {
			continue
		}
		payload[key] = value
	}

	return payload, nil
}

func indexedConfigFieldKey(key string) (string, int, bool) {
	if !strings.HasSuffix(key, "]") {
		return "", 0, false
	}
	openIndex := strings.LastIndex(key, "[")
	if openIndex <= 0 || openIndex >= len(key)-1 {
		return "", 0, false
	}
	name := strings.TrimSpace(key[:openIndex])
	index, err := strconv.Atoi(strings.TrimSuffix(key[openIndex+1:], "]"))
	if err != nil || name == "" {
		return "", 0, false
	}
	return name, index, true
}

func ticketReplyStatusInputs(inputs map[string]string) []int64 {
	values, err := jsonNumberSliceToInt64(indexedJSONNumbers(inputs, "reply_status"))
	if err == nil && len(values) > 0 {
		return values
	}

	raw, ok := inputs["reply_status"]
	if !ok {
		return nil
	}
	parts := parseStringList(raw)
	result := make([]int64, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err == nil {
			result = append(result, value)
		}
	}
	return result
}

func parseStringList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	var values []string
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &values); err == nil {
			return values
		}
	}

	parts := strings.Split(raw, ",")
	values = make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(strings.Trim(part, `"'`))
		if part != "" {
			values = append(values, part)
		}
	}
	return values
}

func readManagedServerSortPayload(r *http.Request) (map[string]map[int64]int64, error) {
	payload := make(map[string]any)
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		if err := readJSONBody(r, &payload); err != nil {
			return nil, err
		}
	}

	inputs, err := readInputs(r)
	if err != nil {
		return nil, err
	}

	result := make(map[string]map[int64]int64)
	for serverType, rawItems := range payload {
		items, ok := rawItems.(map[string]any)
		if !ok {
			continue
		}
		for rawID, rawSort := range items {
			id, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
			if err != nil {
				continue
			}
			sortValue, ok := managedSortAnyToInt64(rawSort)
			if !ok {
				continue
			}
			if _, ok := result[serverType]; !ok {
				result[serverType] = map[int64]int64{}
			}
			result[serverType][id] = sortValue
		}
	}

	for key, value := range inputs {
		serverType, id, ok := managedServerSortInputKey(key)
		if !ok {
			continue
		}
		sortValue, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			continue
		}
		if _, exists := result[serverType]; !exists {
			result[serverType] = map[int64]int64{}
		}
		result[serverType][id] = sortValue
	}

	return result, nil
}

func readManagedServerPayload(r *http.Request) (map[string]any, error) {
	return readStructuredInputs(r)
}

func managedSortAnyToInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		next, err := typed.Int64()
		if err == nil {
			return next, true
		}
		floatValue, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return int64(floatValue), true
	case float64:
		return int64(typed), true
	case float32:
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case string:
		next, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return next, err == nil
	default:
		next, err := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(value)), 10, 64)
		return next, err == nil
	}
}

func managedServerSortInputKey(key string) (string, int64, bool) {
	for _, prefix := range []string{"shadowsocks", "vmess", "vless", "trojan", "tuic", "hysteria", "anytls", "v2node"} {
		if !strings.HasPrefix(key, prefix+"[") || !strings.HasSuffix(key, "]") {
			continue
		}
		rawID := strings.TrimSuffix(strings.TrimPrefix(key, prefix+"["), "]")
		id, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
		if err != nil {
			return "", 0, false
		}
		return prefix, id, true
	}
	return "", 0, false
}

func int64SliceFromValues(values []any) ([]int64, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := make([]int64, 0, len(values))
	for _, value := range values {
		raw := strings.TrimSpace(fmt.Sprint(value))
		if raw == "" {
			continue
		}
		next, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, err
		}
		result = append(result, next)
	}
	return result, nil
}

func stringSliceFromValues(values []any) []string {
	if len(values) == 0 {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		raw := strings.TrimSpace(fmt.Sprint(value))
		if raw != "" {
			result = append(result, raw)
		}
	}
	return result
}

func requiredOptionalInt64(raw string) (*int64, bool, error) {
	value, err := parseOptionalInt64(raw)
	if strings.TrimSpace(raw) == "" {
		return nil, false, err
	}
	return value, true, err
}

var allowedAdminOrderPeriods = []string{
	"month_price",
	"quarter_price",
	"half_year_price",
	"year_price",
	"two_year_price",
	"three_year_price",
	"onetime_price",
	"reset_price",
}

func parseOptionalInt64(raw string) (*int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func readLegacyStatRange(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return 0, 0, false
	}
	startAt, endAt, ok := legacyStatRangeFromInputs(inputs)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "参数有误"})
		return 0, 0, false
	}
	return startAt, endAt, true
}

func legacyStatRangeFromInputs(inputs map[string]string) (int64, int64, bool) {
	startAt, endAt := currentDayRangeUnix()
	startRaw := strings.TrimSpace(inputs["start_at"])
	if startRaw == "" {
		startRaw = strings.TrimSpace(inputs["startAt"])
	}
	if startRaw != "" {
		parsed, err := strconv.ParseInt(startRaw, 10, 64)
		if err != nil || parsed <= 0 {
			return 0, 0, false
		}
		startAt = parsed
	}
	endRaw := strings.TrimSpace(inputs["end_at"])
	if endRaw == "" {
		endRaw = strings.TrimSpace(inputs["endAt"])
	}
	if endRaw != "" {
		parsed, err := strconv.ParseInt(endRaw, 10, 64)
		if err != nil || parsed <= 0 {
			return 0, 0, false
		}
		endAt = parsed
	}
	return startAt, endAt, endAt > startAt
}

func currentDayRangeUnix() (int64, int64) {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return start.Unix(), start.Add(24 * time.Hour).Unix()
}

func containsInt64(values []int64, target int64) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func parseBoolish(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func indexedOrderFilters(inputs map[string]string, prefix string) []admin.OrderFilter {
	entries := indexedNestedFieldMap(inputs, prefix)
	result := make([]admin.OrderFilter, 0, len(entries))
	for _, entry := range entries {
		result = append(result, admin.OrderFilter{
			Key:       strings.TrimSpace(entry["key"]),
			Condition: strings.TrimSpace(entry["condition"]),
			Value:     strings.TrimSpace(entry["value"]),
		})
	}
	return result
}

func userFilters(inputs map[string]string) []admin.UserFilter {
	entries := indexedNestedFieldMap(inputs, "filter")
	result := make([]admin.UserFilter, 0, len(entries))
	for _, entry := range entries {
		result = append(result, admin.UserFilter{
			Key:       strings.TrimSpace(entry["key"]),
			Condition: strings.TrimSpace(entry["condition"]),
			Value:     strings.TrimSpace(entry["value"]),
		})
	}
	return result
}

func enforceStaffUserScope(filters []admin.UserFilter) []admin.UserFilter {
	result := append([]admin.UserFilter{}, filters...)
	result = append(result,
		admin.UserFilter{Key: "is_admin", Condition: "=", Value: "0"},
		admin.UserFilter{Key: "is_staff", Condition: "=", Value: "0"},
	)
	return result
}

func copyInputsWithout(inputs map[string]string, keys ...string) map[string]string {
	if len(inputs) == 0 {
		return map[string]string{}
	}
	skip := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		skip[key] = struct{}{}
	}
	result := make(map[string]string, len(inputs))
	for key, value := range inputs {
		if _, ok := skip[key]; ok {
			continue
		}
		result[key] = value
	}
	return result
}

func indexedNestedFieldMap(inputs map[string]string, prefix string) []map[string]string {
	grouped := make(map[int]map[string]string)
	indices := make([]int, 0)
	for key, value := range inputs {
		index, name, ok := nestedIndexedFieldKey(key, prefix)
		if !ok {
			continue
		}
		if _, exists := grouped[index]; !exists {
			grouped[index] = map[string]string{}
			indices = append(indices, index)
		}
		grouped[index][name] = value
	}
	slices.Sort(indices)

	result := make([]map[string]string, 0, len(indices))
	for _, index := range indices {
		result = append(result, grouped[index])
	}
	return result
}

func nestedIndexedFieldKey(key, prefix string) (int, string, bool) {
	if !strings.HasPrefix(key, prefix+"[") {
		return 0, "", false
	}
	rest := strings.TrimPrefix(key, prefix+"[")
	closeIndex := strings.Index(rest, "]")
	if closeIndex <= 0 {
		return 0, "", false
	}
	index, err := strconv.Atoi(rest[:closeIndex])
	if err != nil {
		return 0, "", false
	}
	rest = rest[closeIndex+1:]
	if !strings.HasPrefix(rest, "[") || !strings.HasSuffix(rest, "]") {
		return 0, "", false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(rest, "["), "]")
	if strings.TrimSpace(name) == "" {
		return 0, "", false
	}
	return index, name, true
}

func isStaffManageableUser(data map[string]any) bool {
	if len(data) == 0 {
		return false
	}
	return anyInt64(data["is_admin"]) == 0 && anyInt64(data["is_staff"]) == 0
}

func anyInt64(value any) int64 {
	switch typed := value.(type) {
	case nil:
		return 0
	case int:
		return int64(typed)
	case int8:
		return int64(typed)
	case int16:
		return int64(typed)
	case int32:
		return int64(typed)
	case int64:
		return typed
	case uint:
		return int64(typed)
	case uint8:
		return int64(typed)
	case uint16:
		return int64(typed)
	case uint32:
		return int64(typed)
	case uint64:
		return int64(typed)
	case float32:
		return int64(typed)
	case float64:
		return int64(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return parsed
		}
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}

func shouldServeStatic(publicDir, requestPath string) bool {
	if requestPath == "/" {
		return false
	}
	cleanPath := strings.TrimPrefix(requestPath, "/")
	if cleanPath == "" {
		return false
	}

	if strings.HasPrefix(cleanPath, "api/") {
		return false
	}

	_, err := os.Stat(publicDir + "/" + cleanPath)
	return err == nil
}
