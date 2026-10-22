package usermigration

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var testHash = func() string {
	b, e := bcrypt.GenerateFromPassword([]byte("test-password-not-a-real-account"), bcrypt.MinCost)
	if e != nil {
		panic(e)
	}
	return string(b)
}()

func i64(n int64) *int64 { return &n }
func sampleUser(id int64) sourceUser {
	return sourceUser{ID: id, Email: fmt.Sprintf("person%d@example.test", id), Password: testHash, Balance: 1234, CommissionBalance: 567, U: 123, D: 456, T: 1700000000, TransferEnable: 123456789, PlanID: i64(4), ExpiredAt: i64(1900000000), CreatedAt: 1700000000, UpdatedAt: 1700000010, Token: fmt.Sprintf("%032d", id), UUID: fmt.Sprintf("00000000-0000-4000-a000-%012d", id)}
}
func sampleTarget() targetState {
	return targetState{plans: map[int64]Plan{9: {ID: 9, Name: "Target", GroupID: 3, TransferEnable: 500, DeviceLimit: i64(4), SpeedLimit: i64(100), ResetTrafficMethod: i64(1)}}, emails: map[string]bool{}, tokens: map[string]bool{}, uuids: map[string]bool{}}
}

var fixedNow = time.Date(2026, time.October, 22, 8, 0, 0, 0, time.UTC)

func TestPreviewPreservesAssetsAndMapsOnlyPlanSettings(t *testing.T) {
	src := sampleUser(1)
	out, users, plans, err := buildPreview(snapshot{users: []sourceUser{src}}, sampleTarget(), map[int64]int64{4: 9}, "discard", fixedNow)
	if err != nil || !out.CanApply || len(users) != 1 {
		t.Fatalf("preview = %+v err=%v", out, err)
	}
	u := users[0]
	if u.Balance != 1234 || u.CommissionBalance != 567 || u.U != 123 || u.D != 456 || u.T != 1700000000 || u.TransferEnable != 123456789 || u.ExpiredAt == nil || *u.ExpiredAt != 1900000000 {
		t.Fatal("source assets were changed")
	}
	if u.TargetPlan.ID != 9 || u.TargetPlan.GroupID != 3 || *u.TargetPlan.DeviceLimit != 4 || *u.TargetPlan.SpeedLimit != 100 || len(plans) != 1 {
		t.Fatal("target plan mapping not applied")
	}
	if u.Password != src.Password || u.Token != src.Token || u.UUID != src.UUID {
		t.Fatal("compatible account identity not preserved")
	}
	data, _ := json.Marshal(out)
	for _, secret := range []string{src.Email, src.Password, src.Token, src.UUID} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("preview exposed a credential: %s", secret)
		}
	}
}
func TestNoPlanPreservesAssetsWithoutGrantingPlan(t *testing.T) {
	for _, expiry := range []*int64{nil, i64(1900000000), i64(0)} {
		src := sampleUser(1)
		src.PlanID = nil
		src.ExpiredAt = expiry
		out, users, _, err := buildPreview(snapshot{users: []sourceUser{src}}, sampleTarget(), nil, "skip", fixedNow)
		if err != nil || !out.CanApply || out.Counts.NoPlan != 1 || users[0].TargetPlan != nil || users[0].PlanID != nil || users[0].TransferEnable != src.TransferEnable || !reflect.DeepEqual(users[0].ExpiredAt, src.ExpiredAt) {
			t.Fatalf("unpaid account gained rights: %+v %v", users, err)
		}
		if users[0].U != src.U || users[0].D != src.D {
			t.Fatal("usage was changed")
		}
	}
	src := sampleUser(1)
	src.ExpiredAt = nil
	_, users, _, _ := buildPreview(snapshot{users: []sourceUser{src}}, sampleTarget(), map[int64]int64{4: 9}, "skip", fixedNow)
	if users[0].ExpiredAt != nil {
		t.Fatal("existing purchased permanent account lost exact NULL expiry")
	}
}
func TestPrivilegedExistingDuplicateAndIdentityCollision(t *testing.T) {
	target := sampleTarget()
	target.emails["person3@example.test"] = true
	target.tokens[sampleUser(4).Token] = true
	target.uuids[sampleUser(4).UUID] = true
	a, b, c, d, e := sampleUser(1), sampleUser(2), sampleUser(3), sampleUser(4), sampleUser(5)
	a.IsAdmin = 1
	b.IsStaff = 1
	c.Email = " PERSON3@EXAMPLE.TEST "
	e.Email = " PERSON4@EXAMPLE.TEST "
	out, users, _, err := buildPreview(snapshot{users: []sourceUser{a, b, c, d, e}}, target, map[int64]int64{4: 9}, "skip", fixedNow)
	if err != nil || !out.CanApply || out.Counts.SkippedPrivileged != 2 || out.Counts.SkippedExisting != 1 || out.Counts.SkippedDuplicateEmail != 1 || out.Counts.RegeneratedTokens != 1 || out.Counts.RegeneratedUUIDs != 1 || len(users) != 1 {
		t.Fatalf("counts %+v %v", out.Counts, err)
	}
	if users[0].Token == d.Token || users[0].UUID == d.UUID || !tokenPattern.MatchString(users[0].Token) || !uuidPattern.MatchString(users[0].UUID) {
		t.Fatal("collision was not safely replaced")
	}
}
func TestLockedCommissionPolicies(t *testing.T) {
	for _, policy := range []string{"skip", "discard", "to_balance"} {
		t.Run(policy, func(t *testing.T) {
			src := sampleUser(1)
			src.LockingBalance = 100
			out, users, _, err := buildPreview(snapshot{users: []sourceUser{src}}, sampleTarget(), map[int64]int64{4: 9}, policy, fixedNow)
			if err != nil || out.Counts.LockingBalanceUsers != 1 || out.Counts.LockingBalanceTotal != 100 {
				t.Fatalf("wrong count %+v %v", out, err)
			}
			if policy == "skip" {
				if len(users) != 0 || out.Counts.SkippedLockedBalance != 1 {
					t.Fatal("locked user not skipped")
				}
				return
			}
			want := int64(1234)
			if policy == "to_balance" {
				want += 100
			}
			if !out.CanApply || len(users) != 1 || users[0].Balance != want || users[0].CommissionBalance != 567 || users[0].LockingBalance != 0 {
				t.Fatalf("policy changed wrong asset: %+v", users)
			}
		})
	}
}
func TestPreviewBlocksUnsupportedAndUnmapped(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*sourceUser)
		mapping map[int64]int64
		count   func(Counts) int
	}{
		{"missing-map", func(*sourceUser) {}, nil, func(c Counts) int { return c.UnmappedPlan }},
		{"email", func(u *sourceUser) { u.Email = "not an email" }, map[int64]int64{4: 9}, func(c Counts) int { return c.InvalidEmail }},
		{"password", func(u *sourceUser) { u.Password = "plaintext" }, map[int64]int64{4: 9}, func(c Counts) int { return c.UnsupportedPassword }},
		{"algorithm", func(u *sourceUser) { u.PasswordAlgo = "md5" }, map[int64]int64{4: 9}, func(c Counts) int { return c.UnsupportedPassword }},
		{"balance-overflow", func(u *sourceUser) { u.Balance = math.MaxInt32 + 1 }, map[int64]int64{4: 9}, func(c Counts) int { return c.InvalidValues }},
		{"negative-traffic", func(u *sourceUser) { u.D = -1 }, map[int64]int64{4: 9}, func(c Counts) int { return c.InvalidValues }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := sampleUser(1)
			tc.mutate(&u)
			out, _, _, err := buildPreview(snapshot{users: []sourceUser{u}}, sampleTarget(), tc.mapping, "skip", fixedNow)
			if err != nil || out.CanApply || tc.count(out.Counts) != 1 || len(out.Blockers) == 0 {
				t.Fatalf("unsafe preview allowed: %+v %v", out, err)
			}
		})
	}
	u := sampleUser(1)
	u.Password = "unsupported"
	out, _, _, err := buildPreview(snapshot{users: []sourceUser{u}}, sampleTarget(), map[int64]int64{4: 0}, "skip", fixedNow)
	if err != nil || out.Counts.SkippedPlan != 1 || out.Counts.UnsupportedPassword != 0 {
		t.Fatal("explicit exclusion not honored")
	}
}
func TestToBalanceOverflowIsBlocked(t *testing.T) {
	u := sampleUser(1)
	u.LockingBalance = math.MaxInt32
	out, _, _, _ := buildPreview(snapshot{users: []sourceUser{u}}, sampleTarget(), map[int64]int64{4: 9}, "to_balance", fixedNow)
	if out.Counts.InvalidValues != 1 || out.CanApply {
		t.Fatal("locking sum overflow not blocked")
	}
}
func TestTargetChangesInvalidatePreview(t *testing.T) {
	_, users, plans, err := buildPreview(snapshot{users: []sourceUser{sampleUser(1)}}, sampleTarget(), map[int64]int64{4: 9}, "discard", fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	p := &storedPreview{users: users, plans: plans, resetMethod: 0}
	if err = verifyTarget(p, sampleTarget(), 0); err != nil {
		t.Fatal(err)
	}
	for _, modify := range []func(*targetState){func(s *targetState) { s.emails[users[0].Email] = true }, func(s *targetState) { s.tokens[users[0].Token] = true }, func(s *targetState) { s.uuids[users[0].UUID] = true }, func(s *targetState) { p := s.plans[9]; p.GroupID++; s.plans[9] = p }} {
		target := sampleTarget()
		modify(&target)
		if verifyTarget(p, target, 0) != ErrPreviewStale {
			t.Fatal("target change accepted")
		}
	}
	if verifyTarget(p, sampleTarget(), 4) != ErrPreviewStale {
		t.Fatal("global reset change accepted")
	}
}
func TestSourceConfigValidationAndRedaction(t *testing.T) {
	base := SourceConfig{Host: "127.0.0.1", Database: "xboard", Username: "read_only", Password: "SECRET-UNIQUE-TEST"}
	c, err := normalizedSource(base)
	if err != nil || c.Port != 3306 {
		t.Fatal("default port failed")
	}
	for _, host := range []string{"localhost:3306", "/var/run/mysql.sock", "tcp(localhost)", "localhost\n", "host?multiStatements=true"} {
		c := base
		c.Host = host
		if host == "localhost\n" {
			c.Host = "local\nhost"
		}
		if _, err := normalizedSource(c); err == nil {
			t.Fatalf("unsafe host accepted: %q", host)
		}
	}
	for _, formatted := range []string{fmt.Sprint(base), fmt.Sprintf("%+v", base), fmt.Sprintf("%#v", base)} {
		if strings.Contains(formatted, base.Password) {
			t.Fatal("formatted configuration exposed password")
		}
	}
}

func TestBcryptVariantsAndTokenBlankRegeneration(t *testing.T) {
	for _, prefix := range []string{"$2a$", "$2b$", "$2y$"} {
		u := sampleUser(1)
		u.Password = prefix + u.Password[4:]
		u.Token = ""
		u.UUID = ""
		result, users, _, err := buildPreview(snapshot{users: []sourceUser{u}}, sampleTarget(), map[int64]int64{4: 9}, "skip", fixedNow)
		if err != nil || !result.CanApply || result.Counts.RegeneratedTokens != 1 || result.Counts.RegeneratedUUIDs != 1 || len(users) != 1 {
			t.Fatalf("bcrypt %s / empty identity failed: %+v %v", prefix, result, err)
		}
	}
}
