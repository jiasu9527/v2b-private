package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	demoSeedPrefix       = "[seed-demo]"
	demoSeedPassword     = "Seed123456"
	demoSeedAdminDefault = "admin@example.com"
	demoScheduleKey      = "SCHEDULE_LAST_CHECK_AT_"
)

type demoSeedSpec struct {
	Groups          []demoSeedGroup
	Routes          []demoSeedRoute
	Plans           []demoSeedPlan
	Users           []demoSeedUser
	Payments        []demoSeedPayment
	Coupons         []demoSeedCoupon
	Giftcards       []demoSeedGiftcard
	Notices         []demoSeedNotice
	Knowledge       []demoSeedKnowledge
	Tickets         []demoSeedTicket
	Orders          []demoSeedOrder
	InviteCampaigns []demoSeedInviteCampaign
	CommissionLogs  []demoSeedCommissionLog
	ManagedServers  []demoSeedManagedServer
	StatRecords     []demoSeedStatRecord
	SystemLogs      []demoSeedSystemLog
}

type demoSeedGroup struct {
	Key  string
	Name string
}

type demoSeedRoute struct {
	Key         string
	Remarks     string
	Match       []string
	Action      string
	ActionValue string
}

type demoSeedPlan struct {
	Key             string
	Name            string
	GroupKey        string
	TransferEnable  int64
	DeviceLimit     int64
	SpeedLimit      int64
	Content         string
	MonthPrice      int64
	YearPrice       int64
	ResetPrice      int64
	CapacityLimit   int64
	ResetTrafficWay int64
}

type demoSeedUser struct {
	Key               string
	Email             string
	Password          string
	IsStaff           int64
	IsAdmin           int64
	Banned            int64
	InviteUserKey     string
	GroupKey          string
	PlanKey           string
	Balance           int64
	CommissionRate    int64
	CommissionBalance int64
	TransferEnable    int64
	DeviceLimit       int64
	SpeedLimit        int64
	TrafficUsed       int64
	LastSeenOffsetSec int64
	ExpireOffsetSec   int64
	Remarks           string
}

type demoSeedPayment struct {
	Key                 string
	UUID                string
	Name                string
	Gateway             string
	Config              map[string]string
	NotifyDomain        string
	Enable              int64
	Sort                int64
	HandlingFeeFixed    int64
	HandlingFeePercent  float64
	Icon                string
}

type demoSeedCoupon struct {
	Code      string
	Name      string
	Type      int64
	Value     int64
	Show      int64
	LimitUse  int64
	StartsAt  int64
	EndsAt    int64
}

type demoSeedGiftcard struct {
	Code      string
	Name      string
	Type      int64
	Value     int64
	LimitUse  int64
	StartsAt  int64
	EndsAt    int64
	PlanKey   string
}

type demoSeedNotice struct {
	Title   string
	Content string
	Show    int64
	ImgURL  string
	Tags    []string
}

type demoSeedKnowledge struct {
	Language string
	Category string
	Title    string
	Body     string
	Show     int64
	Sort     int64
}

type demoSeedTicket struct {
	UserKey     string
	Subject     string
	Level       int64
	Status      int64
	ReplyStatus int64
	Messages    []demoSeedTicketMessage
}

type demoSeedTicketMessage struct {
	UserKey  string
	Message  string
}

type demoSeedOrder struct {
	Key                        string
	UserKey                    string
	InviteUserKey              string
	PlanKey                    string
	PaymentKey                 string
	CouponCode                 string
	InviteCampaignKey          string
	TradeNo                    string
	Type                       int64
	Period                     string
	TotalAmount                int64
	DiscountAmount             int64
	InviteCampaignDiscount     int64
	Status                     int64
	CommissionStatus           int64
	CommissionBalance          int64
	CreatedOffsetSec           int64
	PaidOffsetSec              int64
}

type demoSeedInviteCampaign struct {
	Key           string
	UserKey       string
	PlanKey       string
	Period        string
	InviteCode    string
	RewardAmount  int64
	TargetAmount  int64
	CurrentAmount int64
	InviteCount   int64
	Status        int64
	StartOffset   int64
	ExpireOffset  int64
}

type demoSeedCommissionLog struct {
	InviteUserKey string
	UserKey       string
	OrderKey      string
	OrderAmount   int64
	GetAmount     int64
	OffsetSec     int64
}

type demoSeedManagedServer struct {
	ServerType string
	Name       string
	Host       string
	Port       string
	ServerPort int64
	Sort       int64
}

type demoSeedStatRecord struct {
	RecordAt           int64
	OrderCount         int64
	OrderTotal         int64
	CommissionCount    int64
	CommissionTotal    int64
	PaidCount          int64
	PaidTotal          int64
	RegisterCount      int64
	InviteCount        int64
	TransferUsedTotal  string
}

type demoSeedSystemLog struct {
	Title   string
	Level   string
	URI     string
	Method  string
	Data    string
	IP      string
	Context string
}

type demoSeedResult struct {
	AdminEmail        string
	AdminPassword     string
	StaffEmail        string
	StaffPassword     string
	UserEmail         string
	UserPassword      string
	ManagedServerID   int64
	Groups            int
	Routes            int
	Plans             int
	Users             int
	Payments          int
	Coupons           int
	Giftcards         int
	Notices           int
	Knowledge         int
	Tickets           int
	Orders            int
	InviteCampaigns   int
	ManagedServers    int
}

func runSeedDemo(args []string) error {
	flags := flag.NewFlagSet("seed-demo", flag.ContinueOnError)
	dsn := flags.String("dsn", "", "PostgreSQL DSN")
	adminEmail := flags.String("admin-email", strings.TrimSpace(defaultValue(os.Getenv("ADMIN_EMAIL"), demoSeedAdminDefault)), "admin email")
	adminPassword := flags.String("admin-password", defaultValue(os.Getenv("ADMIN_PASSWORD"), "Admin123456"), "admin password")
	if err := flags.Parse(args); err != nil {
		return err
	}

	resolvedDSN, err := resolveDSN(*dsn)
	if err != nil {
		return err
	}
	db, err := openDB(resolvedDSN)
	if err != nil {
		return err
	}
	defer db.Close()

	adminPass, err := upsertAdmin(context.Background(), db, *adminEmail, *adminPassword)
	if err != nil {
		return err
	}

	spec := buildDemoSeedSpec(time.Now().Unix())
	result, err := seedDemo(context.Background(), db, spec)
	if err != nil {
		return err
	}
	result.AdminEmail = strings.TrimSpace(*adminEmail)
	result.AdminPassword = adminPass

	fmt.Printf("demo seed finished: groups=%d routes=%d plans=%d users=%d payments=%d coupons=%d giftcards=%d notices=%d knowledge=%d tickets=%d orders=%d invite_campaigns=%d managed_servers=%d\n",
		result.Groups, result.Routes, result.Plans, result.Users, result.Payments, result.Coupons, result.Giftcards, result.Notices, result.Knowledge, result.Tickets, result.Orders, result.InviteCampaigns, result.ManagedServers)
	fmt.Printf("admin ready: email=%s password=%s\n", result.AdminEmail, result.AdminPassword)
	fmt.Printf("staff ready: email=%s password=%s\n", result.StaffEmail, result.StaffPassword)
	fmt.Printf("user ready: email=%s password=%s\n", result.UserEmail, result.UserPassword)
	fmt.Printf("node smoke target: node_id=%d node_type=vmess\n", result.ManagedServerID)
	return nil
}

func buildDemoSeedSpec(now int64) demoSeedSpec {
	dayStart := now - (now % 86400)
	yesterdayStart := dayStart - 86400

	return demoSeedSpec{
		Groups: []demoSeedGroup{
			{Key: "basic", Name: demoSeedPrefix + " Basic Group"},
			{Key: "vip", Name: demoSeedPrefix + " VIP Group"},
		},
		Routes: []demoSeedRoute{
			{Key: "cn", Remarks: demoSeedPrefix + " CN Direct", Match: []string{"geosite:cn", "geoip:cn"}, Action: "direct"},
		},
		Plans: []demoSeedPlan{
			{Key: "starter", Name: demoSeedPrefix + " Starter Plan", GroupKey: "basic", TransferEnable: 1073741824, DeviceLimit: 3, SpeedLimit: 200, Content: "<p>seed demo starter</p>", MonthPrice: 1299, YearPrice: 12999, ResetPrice: 499, CapacityLimit: 200, ResetTrafficWay: 1},
			{Key: "pro", Name: demoSeedPrefix + " Pro Plan", GroupKey: "vip", TransferEnable: 1879048192, DeviceLimit: 6, SpeedLimit: 500, Content: "<p>seed demo pro</p>", MonthPrice: 2599, YearPrice: 25999, ResetPrice: 999, CapacityLimit: 500, ResetTrafficWay: 2},
		},
		Users: []demoSeedUser{
			{Key: "staff", Email: "seed-demo-staff@example.com", Password: demoSeedPassword, IsStaff: 1, GroupKey: "vip", PlanKey: "pro", Balance: 5000, TransferEnable: 1879048192, DeviceLimit: 6, SpeedLimit: 500, LastSeenOffsetSec: -120, ExpireOffsetSec: 86400 * 90, Remarks: demoSeedPrefix + " staff"},
			{Key: "owner", Email: "seed-demo-owner@example.com", Password: demoSeedPassword, GroupKey: "basic", PlanKey: "starter", Balance: 2500, CommissionRate: 10, CommissionBalance: 800, TransferEnable: 1073741824, DeviceLimit: 3, SpeedLimit: 200, TrafficUsed: 268435456, LastSeenOffsetSec: -60, ExpireOffsetSec: 86400 * 30, Remarks: demoSeedPrefix + " owner"},
			{Key: "invitee", Email: "seed-demo-invitee@example.com", Password: demoSeedPassword, InviteUserKey: "owner", GroupKey: "vip", PlanKey: "pro", Balance: 1500, TransferEnable: 1879048192, DeviceLimit: 6, SpeedLimit: 500, TrafficUsed: 536870912, LastSeenOffsetSec: -180, ExpireOffsetSec: 86400 * 60, Remarks: demoSeedPrefix + " invitee"},
			{Key: "banned", Email: "seed-demo-banned@example.com", Password: demoSeedPassword, Banned: 1, GroupKey: "basic", PlanKey: "starter", TransferEnable: 805306368, DeviceLimit: 1, SpeedLimit: 50, TrafficUsed: 671088640, LastSeenOffsetSec: -7200, ExpireOffsetSec: 86400 * 7, Remarks: demoSeedPrefix + " banned"},
		},
		Payments: []demoSeedPayment{
			{
				Key:                "epay",
				UUID:               "11111111111111111111111111111111",
				Name:               demoSeedPrefix + " EPay",
				Gateway:            "EPay",
				Config:             map[string]string{"url": "https://pay.example.com", "pid": "10001", "key": "seed-demo-key"},
				NotifyDomain:       "https://notify.example.com",
				Enable:             1,
				Sort:               1,
				HandlingFeeFixed:   100,
				HandlingFeePercent: 1.5,
				Icon:               "https://cdn.example.com/seed-demo-epay.svg",
			},
			{
				Key:                "coinpayments",
				UUID:               "22222222222222222222222222222222",
				Name:               demoSeedPrefix + " CoinPayments",
				Gateway:            "CoinPayments",
				Config:             map[string]string{"coinpayments_merchant_id": "merchant-1001", "coinpayments_ipn_secret": "coinpayments-secret", "coinpayments_currency": "USD"},
				NotifyDomain:       "https://notify.example.com",
				Enable:             1,
				Sort:               2,
				HandlingFeeFixed:   0,
				HandlingFeePercent: 0,
				Icon:               "https://cdn.example.com/seed-demo-coinpayments.svg",
			},
			{
				Key:                "stripecheckout",
				UUID:               "33333333333333333333333333333333",
				Name:               demoSeedPrefix + " StripeCheckout",
				Gateway:            "StripeCheckout",
				Config:             map[string]string{"currency": "USD", "stripe_sk_live": "sk_test_seed_demo", "stripe_pk_live": "pk_test_seed_demo", "stripe_webhook_key": "whsec_seed_demo"},
				NotifyDomain:       "https://notify.example.com",
				Enable:             1,
				Sort:               3,
				HandlingFeeFixed:   0,
				HandlingFeePercent: 0,
				Icon:               "https://cdn.example.com/seed-demo-stripe.svg",
			},
		},
		Coupons: []demoSeedCoupon{
			{Code: "SEEDDEMOCOUPON00000000000000001", Name: demoSeedPrefix + " Coupon", Type: 2, Value: 10, Show: 1, LimitUse: 50, StartsAt: yesterdayStart, EndsAt: dayStart + 86400*90},
		},
		Giftcards: []demoSeedGiftcard{
			{Code: "SEEDDEMOGIFTCARD00000000000001", Name: demoSeedPrefix + " Giftcard", Type: 1, Value: 2000, LimitUse: 10, StartsAt: yesterdayStart, EndsAt: dayStart + 86400*90},
		},
		Notices: []demoSeedNotice{
			{Title: demoSeedPrefix + " Notice Visible", Content: "seed demo visible notice", Show: 1, ImgURL: "https://cdn.example.com/seed-demo-banner.png", Tags: []string{"seed", "demo"}},
			{Title: demoSeedPrefix + " Notice Hidden", Content: "seed demo hidden notice", Show: 0, Tags: []string{"seed"}},
		},
		Knowledge: []demoSeedKnowledge{
			{Language: "zh-CN", Category: "guide", Title: demoSeedPrefix + " 入门指南", Body: "seed demo zh guide", Show: 1, Sort: 1},
			{Language: "en-US", Category: "faq", Title: demoSeedPrefix + " FAQ", Body: "seed demo en faq", Show: 1, Sort: 2},
		},
		Tickets: []demoSeedTicket{
			{
				UserKey:     "owner",
				Subject:     demoSeedPrefix + " Ticket",
				Level:       1,
				Status:      0,
				ReplyStatus: 1,
				Messages: []demoSeedTicketMessage{
					{UserKey: "owner", Message: "seed demo user message"},
					{UserKey: "staff", Message: "seed demo staff reply"},
				},
			},
		},
		Orders: []demoSeedOrder{
			{Key: "pending", UserKey: "owner", PlanKey: "starter", PaymentKey: "epay", TradeNo: "seed-demo-order-pending-01", Type: 1, Period: "month_price", TotalAmount: 1299, Status: 0, CommissionStatus: 0, CreatedOffsetSec: -1800},
			{Key: "coinpayments-pending", UserKey: "owner", PlanKey: "starter", PaymentKey: "coinpayments", TradeNo: "seed-demo-order-cpay-pending-01", Type: 1, Period: "month_price", TotalAmount: 1299, Status: 0, CommissionStatus: 0, CreatedOffsetSec: -2400},
			{Key: "stripecheckout-pending", UserKey: "owner", PlanKey: "starter", PaymentKey: "stripecheckout", TradeNo: "seed-demo-order-stchk-pending-01", Type: 1, Period: "month_price", TotalAmount: 1299, Status: 0, CommissionStatus: 0, CreatedOffsetSec: -3000},
			{Key: "cancelled", UserKey: "owner", PlanKey: "starter", PaymentKey: "epay", TradeNo: "seed-demo-order-cancelled-01", Type: 1, Period: "month_price", TotalAmount: 1299, Status: 2, CommissionStatus: 0, CreatedOffsetSec: -7200},
			{Key: "paid", UserKey: "invitee", InviteUserKey: "owner", PlanKey: "pro", PaymentKey: "epay", CouponCode: "SEEDDEMOCOUPON00000000000000001", InviteCampaignKey: "campaign-owner", TradeNo: "seed-demo-order-paid-01", Type: 1, Period: "year_price", TotalAmount: 25999, DiscountAmount: 2599, InviteCampaignDiscount: 500, Status: 3, CommissionStatus: 0, CommissionBalance: 2600, CreatedOffsetSec: -10800, PaidOffsetSec: -9000},
		},
		InviteCampaigns: []demoSeedInviteCampaign{
			{Key: "campaign-owner", UserKey: "owner", PlanKey: "starter", Period: "month_price", InviteCode: "22223333444455556666777788889999", RewardAmount: 1000, TargetAmount: 5000, CurrentAmount: 25999, InviteCount: 1, Status: 1, StartOffset: -86400 * 2, ExpireOffset: 86400 * 5},
		},
		CommissionLogs: []demoSeedCommissionLog{
			{InviteUserKey: "owner", UserKey: "invitee", OrderKey: "paid", OrderAmount: 25999, GetAmount: 2600, OffsetSec: -9000},
		},
		ManagedServers: []demoSeedManagedServer{
			{ServerType: "vmess", Name: demoSeedPrefix + " VMess", Host: "vmess.seed-demo.local", Port: "443", ServerPort: 11001, Sort: 1},
			{ServerType: "trojan", Name: demoSeedPrefix + " Trojan", Host: "trojan.seed-demo.local", Port: "443", ServerPort: 11002, Sort: 2},
			{ServerType: "shadowsocks", Name: demoSeedPrefix + " Shadowsocks", Host: "ss.seed-demo.local", Port: "443", ServerPort: 11003, Sort: 3},
			{ServerType: "tuic", Name: demoSeedPrefix + " TUIC", Host: "tuic.seed-demo.local", Port: "443", ServerPort: 11004, Sort: 4},
			{ServerType: "hysteria", Name: demoSeedPrefix + " Hysteria", Host: "hysteria.seed-demo.local", Port: "443", ServerPort: 11005, Sort: 5},
			{ServerType: "vless", Name: demoSeedPrefix + " VLESS", Host: "vless.seed-demo.local", Port: "443", ServerPort: 11006, Sort: 6},
			{ServerType: "anytls", Name: demoSeedPrefix + " AnyTLS", Host: "anytls.seed-demo.local", Port: "443", ServerPort: 11007, Sort: 7},
			{ServerType: "v2node", Name: demoSeedPrefix + " V2Node", Host: "v2node.seed-demo.local", Port: "443", ServerPort: 11008, Sort: 8},
		},
		StatRecords: []demoSeedStatRecord{
			{RecordAt: yesterdayStart, OrderCount: 1, OrderTotal: 1299, CommissionCount: 0, CommissionTotal: 0, PaidCount: 0, PaidTotal: 0, RegisterCount: 1, InviteCount: 0, TransferUsedTotal: "1073741824"},
			{RecordAt: dayStart, OrderCount: 3, OrderTotal: 28597, CommissionCount: 1, CommissionTotal: 2600, PaidCount: 1, PaidTotal: 25999, RegisterCount: 3, InviteCount: 1, TransferUsedTotal: "3221225472"},
		},
		SystemLogs: []demoSeedSystemLog{
			{Title: demoSeedPrefix + " API Error", Level: "error", URI: "/api/v1/localadmin/system/getSystemLog", Method: "GET", Data: `{"source":"seed-demo"}`, IP: "127.0.0.1", Context: `{"scope":"seed-demo"}`},
			{Title: demoSeedPrefix + " Payment Check", Level: "info", URI: "/api/v1/localadmin/payment/fetch", Method: "GET", Data: `{"source":"seed-demo"}`, IP: "127.0.0.1", Context: `{"scope":"seed-demo"}`},
		},
	}
}

func seedDemo(ctx context.Context, db *sql.DB, spec demoSeedSpec) (demoSeedResult, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return demoSeedResult{}, fmt.Errorf("begin demo seed transaction: %w", err)
	}
	defer tx.Rollback()

	if err := cleanupDemoSeed(ctx, tx, spec); err != nil {
		return demoSeedResult{}, err
	}

	groupIDs := map[string]int64{}
	for _, item := range spec.Groups {
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO v2_server_group (name, created_at, updated_at)
VALUES ($1, $2, $2)
RETURNING id`, item.Name, time.Now().Unix()).Scan(&id); err != nil {
			return demoSeedResult{}, fmt.Errorf("insert demo group %s: %w", item.Name, err)
		}
		groupIDs[item.Key] = id
	}

	routeIDs := map[string]int64{}
	for _, item := range spec.Routes {
		var id int64
		actionValue := nullableString(item.ActionValue)
		matchRaw := mustJSON(item.Match)
		if err := tx.QueryRowContext(ctx, `INSERT INTO v2_server_route (remarks, "match", action, action_value, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $5)
RETURNING id`, item.Remarks, matchRaw, item.Action, actionValue, time.Now().Unix()).Scan(&id); err != nil {
			return demoSeedResult{}, fmt.Errorf("insert demo route %s: %w", item.Remarks, err)
		}
		routeIDs[item.Key] = id
	}

	planIDs := map[string]int64{}
	for _, item := range spec.Plans {
		groupID := groupIDs[item.GroupKey]
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO v2_plan (
group_id, transfer_enable, device_limit, name, speed_limit, "show", sort, renew, content,
month_price, quarter_price, half_year_price, year_price, two_year_price, three_year_price,
onetime_price, reset_price, reset_traffic_method, capacity_limit, created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5, 1, NULL, 1, $6,
$7, NULL, NULL, $8, NULL, NULL,
NULL, $9, $10, $11, $12, $12
) RETURNING id`,
			groupID,
			item.TransferEnable,
			item.DeviceLimit,
			item.Name,
			item.SpeedLimit,
			item.Content,
			item.MonthPrice,
			item.YearPrice,
			item.ResetPrice,
			item.ResetTrafficWay,
			item.CapacityLimit,
			time.Now().Unix(),
		).Scan(&id); err != nil {
			return demoSeedResult{}, fmt.Errorf("insert demo plan %s: %w", item.Name, err)
		}
		planIDs[item.Key] = id
	}

	userIDs := map[string]int64{}
	userPasswords := map[string]string{}
	for _, item := range spec.Users {
		hash, err := bcrypt.GenerateFromPassword([]byte(item.Password), bcrypt.DefaultCost)
		if err != nil {
			return demoSeedResult{}, fmt.Errorf("hash demo user password %s: %w", item.Email, err)
		}
		uuid, err := randomUUID()
		if err != nil {
			return demoSeedResult{}, err
		}
		token, err := randomTokenHex(16)
		if err != nil {
			return demoSeedResult{}, err
		}
		inviteUserID := nullInt64Value(userIDs[item.InviteUserKey])
		groupID := nullInt64Value(groupIDs[item.GroupKey])
		planID := nullInt64Value(planIDs[item.PlanKey])
		expiredAt := time.Now().Unix() + item.ExpireOffsetSec
		lastSeen := time.Now().Unix() + item.LastSeenOffsetSec
		usedU := item.TrafficUsed / 2
		usedD := item.TrafficUsed - usedU
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO v2_user (
invite_user_id, telegram_id, email, password, password_algo, password_salt, balance, discount,
commission_type, commission_rate, commission_balance, t, u, d, transfer_enable, device_limit,
banned, is_admin, last_login_at, is_staff, last_login_ip, uuid, group_id, plan_id, speed_limit,
auto_renewal, remind_expire, remind_traffic, token, expired_at, remarks, created_at, updated_at
) VALUES (
$1, NULL, $2, $3, NULL, NULL, $4, NULL,
0, $5, $6, $7, $8, $9, $10, $11,
$12, $13, NULL, $14, NULL, $15, $16, $17, $18,
1, 1, 1, $19, $20, $21, $22, $22
) RETURNING id`,
			inviteUserID,
			item.Email,
			string(hash),
			item.Balance,
			nullInt64Value(item.CommissionRate),
			item.CommissionBalance,
			lastSeen,
			usedU,
			usedD,
			item.TransferEnable,
			nullInt64Value(item.DeviceLimit),
			item.Banned,
			item.IsAdmin,
			item.IsStaff,
			uuid,
			groupID,
			planID,
			nullInt64Value(item.SpeedLimit),
			token,
			expiredAt,
			item.Remarks,
			time.Now().Unix(),
		).Scan(&id); err != nil {
			return demoSeedResult{}, fmt.Errorf("insert demo user %s: %w", item.Email, err)
		}
		userIDs[item.Key] = id
		userPasswords[item.Key] = item.Password
	}

	inviteCodeIDs := map[string]int64{}
	for _, item := range spec.InviteCampaigns {
		var inviteCodeID int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO v2_invite_code (user_id, code, status, invite_campaign_id, pv, created_at, updated_at)
VALUES ($1, $2, 0, NULL, 15, $3, $3)
RETURNING id`, userIDs[item.UserKey], item.InviteCode, time.Now().Unix()).Scan(&inviteCodeID); err != nil {
			return demoSeedResult{}, fmt.Errorf("insert demo invite code %s: %w", item.InviteCode, err)
		}
		inviteCodeIDs[item.Key] = inviteCodeID
	}

	inviteCampaignIDs := map[string]int64{}
	for _, item := range spec.InviteCampaigns {
		var completedAt any
		if item.Status == 1 {
			completedAt = time.Now().Unix() - 7200
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO v2_invite_campaign (
user_id, plan_id, period, invite_code_id, invite_code, reward_amount, target_amount, current_amount,
invite_count, status, started_at, expired_at, completed_at, abandoned_at, used_at, created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5, $6, $7, $8,
$9, $10, $11, $12, $13, NULL, NULL, $14, $14
) RETURNING id`,
			userIDs[item.UserKey],
			planIDs[item.PlanKey],
			item.Period,
			inviteCodeIDs[item.Key],
			item.InviteCode,
			item.RewardAmount,
			item.TargetAmount,
			item.CurrentAmount,
			item.InviteCount,
			item.Status,
			time.Now().Unix()+item.StartOffset,
			time.Now().Unix()+item.ExpireOffset,
			completedAt,
			time.Now().Unix(),
		).Scan(&id); err != nil {
			return demoSeedResult{}, fmt.Errorf("insert demo invite campaign %s: %w", item.Key, err)
		}
		inviteCampaignIDs[item.Key] = id
		if _, err := tx.ExecContext(ctx, `UPDATE v2_invite_code SET invite_campaign_id = $2, updated_at = $3 WHERE id = $1`, inviteCodeIDs[item.Key], id, time.Now().Unix()); err != nil {
			return demoSeedResult{}, fmt.Errorf("link demo invite code %s: %w", item.Key, err)
		}
	}

	paymentIDs := map[string]int64{}
	for _, item := range spec.Payments {
		configRaw := mustJSON(item.Config)
		notifyDomain := nullableString(item.NotifyDomain)
		icon := nullableString(item.Icon)
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO v2_payment (
uuid, payment, name, icon, config, notify_domain, handling_fee_fixed, handling_fee_percent,
enable, sort, created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5, $6, $7, $8,
$9, $10, $11, $11
) RETURNING id`,
			item.UUID,
			item.Gateway,
			item.Name,
			icon,
			configRaw,
			notifyDomain,
			item.HandlingFeeFixed,
			item.HandlingFeePercent,
			item.Enable,
			item.Sort,
			time.Now().Unix(),
		).Scan(&id); err != nil {
			return demoSeedResult{}, fmt.Errorf("insert demo payment %s: %w", item.Name, err)
		}
		paymentIDs[item.Key] = id
	}

	couponIDs := map[string]int64{}
	for _, item := range spec.Coupons {
		limitPlanIDs := mustJSON([]int64{planIDs["starter"], planIDs["pro"]})
		limitPeriod := mustJSON([]string{"month_price", "year_price"})
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO v2_coupon (
code, name, type, value, "show", limit_use, limit_use_with_user, limit_plan_ids, limit_period,
started_at, ended_at, created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5, $6, 1, $7, $8,
$9, $10, $11, $11
) RETURNING id`,
			item.Code,
			item.Name,
			item.Type,
			item.Value,
			item.Show,
			item.LimitUse,
			limitPlanIDs,
			limitPeriod,
			item.StartsAt,
			item.EndsAt,
			time.Now().Unix(),
		).Scan(&id); err != nil {
			return demoSeedResult{}, fmt.Errorf("insert demo coupon %s: %w", item.Code, err)
		}
		couponIDs[item.Code] = id
	}

	for _, item := range spec.Giftcards {
		var planID any
		if item.PlanKey != "" {
			planID = planIDs[item.PlanKey]
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_giftcard (
code, name, type, value, plan_id, limit_use, used_user_ids, started_at, ended_at, created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5, $6, NULL, $7, $8, $9, $9
)`,
			item.Code,
			item.Name,
			item.Type,
			item.Value,
			planID,
			item.LimitUse,
			item.StartsAt,
			item.EndsAt,
			time.Now().Unix(),
		); err != nil {
			return demoSeedResult{}, fmt.Errorf("insert demo giftcard %s: %w", item.Code, err)
		}
	}

	for idx, item := range spec.Notices {
		tags := mustJSON(item.Tags)
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_notice (title, content, "show", img_url, tags, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $6)`,
			item.Title,
			item.Content,
			item.Show,
			nullableString(item.ImgURL),
			tags,
			time.Now().Unix()+int64(idx),
		); err != nil {
			return demoSeedResult{}, fmt.Errorf("insert demo notice %s: %w", item.Title, err)
		}
	}

	for _, item := range spec.Knowledge {
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_knowledge (language, category, title, body, sort, "show", created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`,
			item.Language,
			item.Category,
			item.Title,
			item.Body,
			item.Sort,
			item.Show,
			time.Now().Unix(),
		); err != nil {
			return demoSeedResult{}, fmt.Errorf("insert demo knowledge %s: %w", item.Title, err)
		}
	}

	orderIDs := map[string]int64{}
	for _, item := range spec.Orders {
		var paidAt any
		if item.PaidOffsetSec != 0 {
			paidAt = time.Now().Unix() + item.PaidOffsetSec
		}
		var inviteUserID any
		if item.InviteUserKey != "" {
			inviteUserID = userIDs[item.InviteUserKey]
		}
		var couponID any
		if item.CouponCode != "" {
			couponID = couponIDs[item.CouponCode]
		}
		var inviteCampaignID any
		if item.InviteCampaignKey != "" {
			inviteCampaignID = inviteCampaignIDs[item.InviteCampaignKey]
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO v2_order (
invite_user_id, user_id, plan_id, coupon_id, payment_id, type, period, trade_no, callback_no,
total_amount, handling_amount, discount_amount, surplus_amount, refund_amount, balance_amount,
surplus_order_ids, status, commission_status, commission_balance, actual_commission_balance,
invite_campaign_id, invite_campaign_discount_amount, paid_at, created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5, $6, $7, $8, $9,
$10, NULL, $11, NULL, NULL, NULL,
NULL, $12, $13, $14, NULL,
$15, $16, $17, $18, $18
) RETURNING id`,
			inviteUserID,
			userIDs[item.UserKey],
			planIDs[item.PlanKey],
			couponID,
			paymentIDs[item.PaymentKey],
			item.Type,
			item.Period,
			item.TradeNo,
			callbackNoForStatus(item.Status),
			item.TotalAmount,
			nullInt64Value(item.DiscountAmount),
			item.Status,
			item.CommissionStatus,
			item.CommissionBalance,
			inviteCampaignID,
			item.InviteCampaignDiscount,
			paidAt,
			time.Now().Unix()+item.CreatedOffsetSec,
		).Scan(&id); err != nil {
			return demoSeedResult{}, fmt.Errorf("insert demo order %s: %w", item.TradeNo, err)
		}
		orderIDs[item.Key] = id
	}

	for _, item := range spec.CommissionLogs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_commission_log (invite_user_id, user_id, trade_no, order_amount, get_amount, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $6)`,
			userIDs[item.InviteUserKey],
			userIDs[item.UserKey],
			findOrderTradeNo(spec.Orders, item.OrderKey),
			item.OrderAmount,
			item.GetAmount,
			time.Now().Unix()+item.OffsetSec,
		); err != nil {
			return demoSeedResult{}, fmt.Errorf("insert demo commission log: %w", err)
		}
	}

	for _, item := range spec.Tickets {
		var ticketID int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO v2_ticket (user_id, subject, level, status, reply_status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $6)
RETURNING id`,
			userIDs[item.UserKey],
			item.Subject,
			item.Level,
			item.Status,
			item.ReplyStatus,
			time.Now().Unix(),
		).Scan(&ticketID); err != nil {
			return demoSeedResult{}, fmt.Errorf("insert demo ticket %s: %w", item.Subject, err)
		}
		for idx, message := range item.Messages {
			if _, err := tx.ExecContext(ctx, `INSERT INTO v2_ticket_message (user_id, ticket_id, message, created_at, updated_at)
VALUES ($1, $2, $3, $4, $4)`,
				userIDs[message.UserKey],
				ticketID,
				message.Message,
				time.Now().Unix()+int64(idx),
			); err != nil {
				return demoSeedResult{}, fmt.Errorf("insert demo ticket message: %w", err)
			}
		}
	}

	serverIDs := map[string]int64{}
	for _, item := range spec.ManagedServers {
		id, err := insertDemoManagedServer(ctx, tx, item, groupIDs, routeIDs)
		if err != nil {
			return demoSeedResult{}, err
		}
		serverIDs[item.ServerType] = id
	}

	for _, item := range spec.StatRecords {
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_stat (
record_at, record_type, order_count, order_total, commission_count, commission_total, paid_count, paid_total,
register_count, invite_count, transfer_used_total, created_at, updated_at
) VALUES (
$1, 'd', $2, $3, $4, $5, $6, $7,
$8, $9, $10, $11, $11
) ON CONFLICT (record_at) DO UPDATE SET
order_count = EXCLUDED.order_count,
order_total = EXCLUDED.order_total,
commission_count = EXCLUDED.commission_count,
commission_total = EXCLUDED.commission_total,
paid_count = EXCLUDED.paid_count,
paid_total = EXCLUDED.paid_total,
register_count = EXCLUDED.register_count,
invite_count = EXCLUDED.invite_count,
transfer_used_total = EXCLUDED.transfer_used_total,
updated_at = EXCLUDED.updated_at`,
			item.RecordAt,
			item.OrderCount,
			item.OrderTotal,
			item.CommissionCount,
			item.CommissionTotal,
			item.PaidCount,
			item.PaidTotal,
			item.RegisterCount,
			item.InviteCount,
			item.TransferUsedTotal,
			time.Now().Unix(),
		); err != nil {
			return demoSeedResult{}, fmt.Errorf("upsert demo stat %d: %w", item.RecordAt, err)
		}
	}

	for _, recordAt := range []int64{spec.StatRecords[0].RecordAt, spec.StatRecords[1].RecordAt} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_stat_user (user_id, server_rate, u, d, record_type, record_at, created_at, updated_at)
VALUES ($1, 1.00, $2, $3, 'd', $4, $5, $5)
ON CONFLICT (server_rate, user_id, record_at) DO UPDATE SET u = EXCLUDED.u, d = EXCLUDED.d, updated_at = EXCLUDED.updated_at`,
			userIDs["owner"], 5*1073741824, 3*1073741824, recordAt, time.Now().Unix()); err != nil {
			return demoSeedResult{}, fmt.Errorf("upsert demo stat_user owner: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_stat_user (user_id, server_rate, u, d, record_type, record_at, created_at, updated_at)
VALUES ($1, 1.00, $2, $3, 'd', $4, $5, $5)
ON CONFLICT (server_rate, user_id, record_at) DO UPDATE SET u = EXCLUDED.u, d = EXCLUDED.d, updated_at = EXCLUDED.updated_at`,
			userIDs["invitee"], 7*1073741824, 4*1073741824, recordAt, time.Now().Unix()); err != nil {
			return demoSeedResult{}, fmt.Errorf("upsert demo stat_user invitee: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_stat_server (server_id, server_type, u, d, record_type, record_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, 'd', $5, $6, $6)
ON CONFLICT (server_id, server_type, record_at) DO UPDATE SET u = EXCLUDED.u, d = EXCLUDED.d, updated_at = EXCLUDED.updated_at`,
			serverIDs["vmess"], "vmess", 6*1073741824, 4*1073741824, recordAt, time.Now().Unix()); err != nil {
			return demoSeedResult{}, fmt.Errorf("upsert demo stat_server vmess: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_stat_server (server_id, server_type, u, d, record_type, record_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, 'd', $5, $6, $6)
ON CONFLICT (server_id, server_type, record_at) DO UPDATE SET u = EXCLUDED.u, d = EXCLUDED.d, updated_at = EXCLUDED.updated_at`,
			serverIDs["trojan"], "trojan", 3*1073741824, 2*1073741824, recordAt, time.Now().Unix()); err != nil {
			return demoSeedResult{}, fmt.Errorf("upsert demo stat_server trojan: %w", err)
		}
	}

	for _, item := range spec.SystemLogs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_log (title, level, host, uri, method, data, ip, context, created_at, updated_at)
VALUES ($1, $2, '127.0.0.1', $3, $4, $5, $6, $7, $8, $8)`,
			item.Title,
			item.Level,
			item.URI,
			item.Method,
			item.Data,
			item.IP,
			item.Context,
			time.Now().Unix(),
		); err != nil {
			return demoSeedResult{}, fmt.Errorf("insert demo log %s: %w", item.Title, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO v2_runtime_kv (k, v, expire_at, created_at, updated_at)
VALUES ($1, $2, 0, $3, $3)
ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v, updated_at = EXCLUDED.updated_at`, demoScheduleKey, strconv.FormatInt(time.Now().Unix(), 10), time.Now().Unix()); err != nil {
		return demoSeedResult{}, fmt.Errorf("touch demo schedule heartbeat: %w", err)
	}

	if err := seedDemoRuntimeState(ctx, tx, serverIDs, userIDs); err != nil {
		return demoSeedResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return demoSeedResult{}, fmt.Errorf("commit demo seed: %w", err)
	}

	return demoSeedResult{
		StaffEmail:      findUserEmail(spec.Users, "staff"),
		StaffPassword:   userPasswords["staff"],
		UserEmail:       findUserEmail(spec.Users, "owner"),
		UserPassword:    userPasswords["owner"],
		ManagedServerID: serverIDs["vmess"],
		Groups:          len(spec.Groups),
		Routes:          len(spec.Routes),
		Plans:           len(spec.Plans),
		Users:           len(spec.Users),
		Payments:        len(spec.Payments),
		Coupons:         len(spec.Coupons),
		Giftcards:       len(spec.Giftcards),
		Notices:         len(spec.Notices),
		Knowledge:       len(spec.Knowledge),
		Tickets:         len(spec.Tickets),
		Orders:          len(spec.Orders),
		InviteCampaigns: len(spec.InviteCampaigns),
		ManagedServers:  len(spec.ManagedServers),
	}, nil
}

func cleanupDemoSeed(ctx context.Context, tx *sql.Tx, spec demoSeedSpec) error {
	for _, item := range spec.ManagedServers {
		table := managedServerTableName(item.ServerType)
		if table == "" {
			continue
		}
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM `+table+` WHERE name = $1 LIMIT 1`, item.Name).Scan(&id)
		if err == nil {
			if _, err := tx.ExecContext(ctx, `DELETE FROM v2_stat_server WHERE server_id = $1 AND server_type = $2`, id, item.ServerType); err != nil {
				return fmt.Errorf("cleanup demo stat_server %s: %w", item.Name, err)
			}
			for _, key := range []string{
				managedServerRuntimeKV(item.ServerType, "ONLINE_USER", id),
				managedServerRuntimeKV(item.ServerType, "LAST_CHECK_AT", id),
				managedServerRuntimeKV(item.ServerType, "LAST_PUSH_AT", id),
			} {
				if _, err := tx.ExecContext(ctx, `DELETE FROM v2_runtime_kv WHERE k = $1`, key); err != nil {
					return fmt.Errorf("cleanup demo runtime key %s: %w", key, err)
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE name = $1`, item.Name); err != nil {
			return fmt.Errorf("cleanup demo server %s: %w", item.Name, err)
		}
	}

	for _, item := range spec.Users {
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM v2_user WHERE email = $1 LIMIT 1`, item.Email).Scan(&id)
		if err == nil {
			if _, err := tx.ExecContext(ctx, `DELETE FROM v2_auth_session WHERE user_id = $1`, id); err != nil {
				return fmt.Errorf("cleanup demo auth sessions %s: %w", item.Email, err)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM v2_ticket_message WHERE ticket_id IN (SELECT id FROM v2_ticket WHERE user_id = $1)`, id); err != nil {
				return fmt.Errorf("cleanup demo ticket messages %s: %w", item.Email, err)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM v2_ticket_message WHERE user_id = $1`, id); err != nil {
				return fmt.Errorf("cleanup demo ticket messages by user %s: %w", item.Email, err)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM v2_ticket WHERE user_id = $1`, id); err != nil {
				return fmt.Errorf("cleanup demo tickets %s: %w", item.Email, err)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM v2_invite_campaign_record WHERE invitee_user_id = $1 OR campaign_id IN (SELECT id FROM v2_invite_campaign WHERE user_id = $1)`, id); err != nil {
				return fmt.Errorf("cleanup demo invite records %s: %w", item.Email, err)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM v2_invite_campaign WHERE user_id = $1`, id); err != nil {
				return fmt.Errorf("cleanup demo invite campaigns %s: %w", item.Email, err)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM v2_invite_code WHERE user_id = $1`, id); err != nil {
				return fmt.Errorf("cleanup demo invite codes %s: %w", item.Email, err)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM v2_commission_log WHERE user_id = $1 OR invite_user_id = $1`, id); err != nil {
				return fmt.Errorf("cleanup demo commission logs %s: %w", item.Email, err)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM v2_order WHERE user_id = $1 OR invite_user_id = $1`, id); err != nil {
				return fmt.Errorf("cleanup demo orders %s: %w", item.Email, err)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM v2_stat_user WHERE user_id = $1`, id); err != nil {
				return fmt.Errorf("cleanup demo stat_user %s: %w", item.Email, err)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM v2_runtime_kv WHERE k = $1`, aliveRuntimeKV(id)); err != nil {
				return fmt.Errorf("cleanup demo alive runtime %s: %w", item.Email, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_user WHERE email = $1`, item.Email); err != nil {
			return fmt.Errorf("cleanup demo user %s: %w", item.Email, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM v2_order WHERE trade_no LIKE 'seed-demo-order-%'`); err != nil {
		return fmt.Errorf("cleanup demo orders by trade_no: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM v2_commission_log WHERE trade_no LIKE 'seed-demo-order-%'`); err != nil {
		return fmt.Errorf("cleanup demo commission logs by trade_no: %w", err)
	}

	for _, item := range spec.Payments {
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_payment WHERE uuid = $1 OR name = $2`, item.UUID, item.Name); err != nil {
			return fmt.Errorf("cleanup demo payment %s: %w", item.Name, err)
		}
	}
	for _, item := range spec.Coupons {
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_coupon WHERE code = $1`, item.Code); err != nil {
			return fmt.Errorf("cleanup demo coupon %s: %w", item.Code, err)
		}
	}
	for _, item := range spec.Giftcards {
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_giftcard WHERE code = $1`, item.Code); err != nil {
			return fmt.Errorf("cleanup demo giftcard %s: %w", item.Code, err)
		}
	}
	for _, item := range spec.Notices {
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_notice WHERE title = $1`, item.Title); err != nil {
			return fmt.Errorf("cleanup demo notice %s: %w", item.Title, err)
		}
	}
	for _, item := range spec.Knowledge {
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_knowledge WHERE title = $1`, item.Title); err != nil {
			return fmt.Errorf("cleanup demo knowledge %s: %w", item.Title, err)
		}
	}
	for _, item := range spec.SystemLogs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_log WHERE title = $1`, item.Title); err != nil {
			return fmt.Errorf("cleanup demo log %s: %w", item.Title, err)
		}
	}
	for _, item := range spec.StatRecords {
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_stat WHERE record_at = $1`, item.RecordAt); err != nil {
			return fmt.Errorf("cleanup demo stat %d: %w", item.RecordAt, err)
		}
	}
	for _, item := range spec.Plans {
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_plan WHERE name = $1`, item.Name); err != nil {
			return fmt.Errorf("cleanup demo plan %s: %w", item.Name, err)
		}
	}
	for _, item := range spec.Routes {
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_server_route WHERE remarks = $1`, item.Remarks); err != nil {
			return fmt.Errorf("cleanup demo route %s: %w", item.Remarks, err)
		}
	}
	for _, item := range spec.Groups {
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_server_group WHERE name = $1`, item.Name); err != nil {
			return fmt.Errorf("cleanup demo group %s: %w", item.Name, err)
		}
	}
	return nil
}

func insertDemoManagedServer(ctx context.Context, tx *sql.Tx, item demoSeedManagedServer, groupIDs map[string]int64, routeIDs map[string]int64) (int64, error) {
	now := time.Now().Unix()
	groupID := strconv.FormatInt(groupIDs["basic"], 10)
	if item.ServerType != "vmess" && item.ServerType != "shadowsocks" {
		groupID = strconv.FormatInt(groupIDs["vip"], 10)
	}
	routeID := strconv.FormatInt(routeIDs["cn"], 10)
	tags := `["seed","demo"]`

	switch item.ServerType {
	case "vmess":
		var id int64
		err := tx.QueryRowContext(ctx, `INSERT INTO v2_server_vmess (
group_id, route_id, name, parent_id, host, port, server_port, tls, tags, rate, network, rules,
"networkSettings", "tlsSettings", "ruleSettings", "dnsSettings", "show", sort, created_at, updated_at
) VALUES (
$1, $2, $3, NULL, $4, $5, $6, 0, $7, '1', 'ws', NULL,
$8, '{}', '{}', '{}', 1, $9, $10, $10
) RETURNING id`,
			groupID, routeID, item.Name, item.Host, item.Port, item.ServerPort, tags, `{"path":"/seed-vmess"}`, item.Sort, now).Scan(&id)
		return id, wrapInsertManagedServerErr(item.Name, err)
	case "trojan":
		var id int64
		err := tx.QueryRowContext(ctx, `INSERT INTO v2_server_trojan (
group_id, route_id, parent_id, tags, name, rate, host, port, server_port, network, network_settings,
allow_insecure, server_name, "show", sort, created_at, updated_at
) VALUES (
$1, $2, NULL, $3, $4, '1', $5, $6, $7, 'tcp', '{}',
0, $8, 1, $9, $10, $10
) RETURNING id`,
			groupID, routeID, tags, item.Name, item.Host, item.Port, item.ServerPort, item.Host, item.Sort, now).Scan(&id)
		return id, wrapInsertManagedServerErr(item.Name, err)
	case "shadowsocks":
		var id int64
		err := tx.QueryRowContext(ctx, `INSERT INTO v2_server_shadowsocks (
group_id, route_id, parent_id, tags, name, rate, host, port, server_port, cipher, obfs, obfs_settings,
"show", sort, created_at, updated_at
) VALUES (
$1, $2, NULL, $3, $4, '1', $5, $6, $7, 'aes-128-gcm', NULL, NULL,
1, $8, $9, $9
) RETURNING id`,
			groupID, routeID, tags, item.Name, item.Host, item.Port, item.ServerPort, item.Sort, now).Scan(&id)
		return id, wrapInsertManagedServerErr(item.Name, err)
	case "tuic":
		var id int64
		err := tx.QueryRowContext(ctx, `INSERT INTO v2_server_tuic (
group_id, route_id, name, parent_id, host, port, server_port, tags, rate, "show", sort,
server_name, insecure, disable_sni, udp_relay_mode, zero_rtt_handshake, congestion_control, created_at, updated_at
) VALUES (
$1, $2, $3, NULL, $4, $5, $6, $7, '1.2', 1, $8,
$9, 0, 0, 'native', 0, 'bbr', $10, $10
) RETURNING id`,
			groupID, routeID, item.Name, item.Host, item.Port, item.ServerPort, tags, item.Sort, item.Host, now).Scan(&id)
		return id, wrapInsertManagedServerErr(item.Name, err)
	case "hysteria":
		var id int64
		err := tx.QueryRowContext(ctx, `INSERT INTO v2_server_hysteria (
version, group_id, route_id, name, parent_id, host, port, server_port, tags, rate, "show", sort,
up_mbps, down_mbps, obfs, obfs_password, server_name, insecure, created_at, updated_at
) VALUES (
2, $1, $2, $3, NULL, $4, $5, $6, $7, '1', 1, $8,
200, 200, 'salamander', 'seed-demo-obfs', $9, 0, $10, $10
) RETURNING id`,
			groupID, routeID, item.Name, item.Host, item.Port, item.ServerPort, tags, item.Sort, item.Host, now).Scan(&id)
		return id, wrapInsertManagedServerErr(item.Name, err)
	case "vless":
		var id int64
		port, _ := strconv.ParseInt(item.Port, 10, 64)
		err := tx.QueryRowContext(ctx, `INSERT INTO v2_server_vless (
group_id, route_id, name, parent_id, host, port, server_port, tls, tls_settings, flow, network,
network_settings, encryption, encryption_settings, tags, rate, "show", sort, created_at, updated_at
) VALUES (
$1, $2, $3, NULL, $4, $5, $6, 0, '{}', NULL, 'ws',
$7, 'none', NULL, $8, '1', 1, $9, $10, $10
) RETURNING id`,
			groupID, routeID, item.Name, item.Host, port, item.ServerPort, `{"path":"/seed-vless"}`, tags, item.Sort, now).Scan(&id)
		return id, wrapInsertManagedServerErr(item.Name, err)
	case "anytls":
		var id int64
		err := tx.QueryRowContext(ctx, `INSERT INTO v2_server_anytls (
group_id, route_id, name, parent_id, host, port, server_port, tags, rate, "show", sort,
server_name, insecure, padding_scheme, created_at, updated_at
) VALUES (
$1, $2, $3, NULL, $4, $5, $6, $7, '1', 1, $8,
$9, 0, $10, $11, $11
) RETURNING id`,
			groupID, routeID, item.Name, item.Host, item.Port, item.ServerPort, tags, item.Sort, item.Host, `["stop=8"]`, now).Scan(&id)
		return id, wrapInsertManagedServerErr(item.Name, err)
	case "v2node":
		var id int64
		err := tx.QueryRowContext(ctx, `INSERT INTO v2_server_v2node (
group_id, route_id, name, parent_id, host, listen_ip, port, server_port, tags, rate, "show", sort,
protocol, tls, tls_settings, flow, network, network_settings, encryption, encryption_settings,
disable_sni, udp_relay_mode, zero_rtt_handshake, congestion_control, cipher, up_mbps, down_mbps,
obfs, obfs_password, padding_scheme, created_at, updated_at
) VALUES (
$1, $2, $3, NULL, $4, '0.0.0.0', $5, $6, $7, '1', 1, $8,
'vmess', 0, '{}', NULL, 'ws', $9, NULL, NULL,
0, 'native', 0, 'bbr', NULL, 300, 300,
NULL, NULL, NULL, $10, $10
) RETURNING id`,
			groupID, routeID, item.Name, item.Host, item.Port, item.ServerPort, tags, item.Sort, `{"path":"/seed-v2node"}`, now).Scan(&id)
		return id, wrapInsertManagedServerErr(item.Name, err)
	default:
		return 0, fmt.Errorf("unsupported demo managed server type %s", item.ServerType)
	}
}

func seedDemoRuntimeState(ctx context.Context, tx *sql.Tx, serverIDs map[string]int64, userIDs map[string]int64) error {
	now := time.Now().Unix()
	for serverType, serverID := range serverIDs {
		for suffix, value := range map[string]string{
			"ONLINE_USER":   "2",
			"LAST_CHECK_AT": strconv.FormatInt(now, 10),
			"LAST_PUSH_AT":  strconv.FormatInt(now, 10),
		} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO v2_runtime_kv (k, v, expire_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $4)
ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v, expire_at = EXCLUDED.expire_at, updated_at = EXCLUDED.updated_at`,
				managedServerRuntimeKV(serverType, suffix, serverID),
				value,
				now+3600,
				now,
			); err != nil {
				return fmt.Errorf("seed demo runtime kv %s %s: %w", serverType, suffix, err)
			}
		}
	}

	for _, item := range []struct {
		userKey     string
		serverType  string
		serverID    int64
		ips         []string
	}{
		{userKey: "owner", serverType: "vmess", serverID: serverIDs["vmess"], ips: []string{"1.1.1.1_1", "2.2.2.2_1"}},
		{userKey: "invitee", serverType: "trojan", serverID: serverIDs["trojan"], ips: []string{"3.3.3.3_1"}},
	} {
		state := map[string]any{
			"alive_ip": int64(len(item.ips)),
			item.serverType + strconv.FormatInt(item.serverID, 10): map[string]any{
				"aliveips":     item.ips,
				"lastupdateAt": now,
			},
		}
		raw := mustJSON(state)
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_runtime_kv (k, v, expire_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $4)
ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v, expire_at = EXCLUDED.expire_at, updated_at = EXCLUDED.updated_at`,
			aliveRuntimeKV(userIDs[item.userKey]),
			raw,
			now+120,
			now,
		); err != nil {
			return fmt.Errorf("seed demo alive runtime %s: %w", item.userKey, err)
		}
	}
	return nil
}

func managedServerTableName(serverType string) string {
	switch serverType {
	case "vmess":
		return "v2_server_vmess"
	case "trojan":
		return "v2_server_trojan"
	case "shadowsocks":
		return "v2_server_shadowsocks"
	case "tuic":
		return "v2_server_tuic"
	case "hysteria":
		return "v2_server_hysteria"
	case "vless":
		return "v2_server_vless"
	case "anytls":
		return "v2_server_anytls"
	case "v2node":
		return "v2_server_v2node"
	default:
		return ""
	}
}

func managedServerRuntimeKV(serverType, suffix string, id int64) string {
	return "SERVER_" + strings.ToUpper(strings.TrimSpace(serverType)) + "_" + suffix + "_" + strconv.FormatInt(id, 10)
}

func aliveRuntimeKV(userID int64) string {
	return "ALIVE_IP_USER_" + strconv.FormatInt(userID, 10)
}

func wrapInsertManagedServerErr(name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("insert demo managed server %s: %w", name, err)
}

func mustJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullInt64Value(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func callbackNoForStatus(status int64) any {
	if status == 3 {
		return "seed-demo-paid"
	}
	return nil
}

func findOrderTradeNo(items []demoSeedOrder, key string) string {
	for _, item := range items {
		if item.Key == key {
			return item.TradeNo
		}
	}
	return ""
}

func findUserEmail(items []demoSeedUser, key string) string {
	for _, item := range items {
		if item.Key == key {
			return item.Email
		}
	}
	return ""
}
