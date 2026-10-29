package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
)

func TestServerRegistrationPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("dedicated disposable FOREST_AIOPS_TEST_DSN required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	s := NewService(config.Config{AppKey: "server-registration-fixture"}, db, admin.NewDBService(config.Config{}, db))
	defer s.Close()
	if err := s.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var actor, other, session, otherSession int64
	marker := fmt.Sprint(time.Now().UnixNano())
	for i, dst := range []*int64{&actor, &other} {
		if err := db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at)VALUES($1,'unused','fixture',$2,1,1,1)RETURNING id`, fmt.Sprintf("register-%d-%s@example.test", i, marker), fmt.Sprintf("register-%d-%s", i, marker)).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	defer db.Exec(`DELETE FROM v2_user WHERE id IN($1,$2)`, actor, other)
	defer db.Exec(`DELETE FROM v2_ai_server WHERE actor_id IN($1,$2)`, actor, other)
	for _, dst := range []*int64{&session, &otherSession} {
		if err := db.QueryRow(`INSERT INTO v2_ai_session(title,model,created_by,created_at,updated_at)VALUES('register','fixture',$1,1,1)RETURNING id`, actor).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	defer db.Exec(`DELETE FROM v2_ai_session WHERE id IN($1,$2)`, session, otherSession)
	if _, err := s.SaveConfig(ctx, actor, ConfigSaveRequest{Enabled: true, BaseURL: "https://fixture.example.test/v1", Models: DefaultModels, APIKey: "fixture"}); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_ai_config WHERE updated_by=$1`, actor)
	var turn int64
	if err := db.QueryRow(`INSERT INTO v2_ai_turn(session_id,actor_id,request_id,request_hash,model,status,created_at,expires_at)VALUES($1,$2,$3,'fixture','fixture','completed',$4,$5)RETURNING id`, session, actor, "register-turn-"+marker, time.Now().Unix(), time.Now().Add(time.Hour).Unix()).Scan(&turn); err != nil {
		t.Fatal(err)
	}
	password, err := s.encryptSecret("secret-test")
	if err != nil {
		t.Fatal(err)
	}
	newIntake := func(host string, port int, pin string) int64 {
		t.Helper()
		var id, intakeTurn int64
		if err := db.QueryRow(`INSERT INTO v2_ai_turn(session_id,actor_id,request_id,request_hash,model,status,created_at,expires_at)VALUES($1,$2,$3,'fixture','fixture','completed',$4,$5)RETURNING id`, session, actor, fmt.Sprintf("register-intake-%d", time.Now().UnixNano()), time.Now().Unix(), time.Now().Add(time.Hour).Unix()).Scan(&intakeTurn); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`INSERT INTO v2_ai_server_intake(session_id,turn_id,actor_id,name,host,port,username,host_key_sha256,password_cipher,created_at,expires_at)VALUES($1,$2,$3,'fixture landing',$4,$5,'root',$6,$7,$8,$9)RETURNING id`, session, intakeTurn, actor, host, port, pin, password, time.Now().Unix(), time.Now().Add(time.Hour).Unix()).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	call := func(actorID, sessionID, intakeID int64, callID string) (ToolOutcome, error) {
		raw, _ := json.Marshal(map[string]any{"intake_id": intakeID, "reason": "register fixture"})
		return s.callTool(ctx, actorID, sessionID, ToolCall{ID: callID, Function: ToolFunction{Name: "ssh_server_register", Arguments: string(raw)}})
	}
	getAction := func(id int64) Action {
		t.Helper()
		a, e := scanAction(db.QueryRow(`SELECT `+actionColumns+` FROM v2_ai_action WHERE id=$1`, id))
		if e != nil {
			t.Fatal(e)
		}
		return a
	}
	getServerID := func(output string) int64 {
		t.Helper()
		var v struct {
			ServerID int64 `json:"server_id"`
		}
		if json.Unmarshal([]byte(output), &v) != nil || v.ServerID <= 0 {
			t.Fatalf("missing server id: %s", output)
		}
		return v.ServerID
	}
	assertNoCredentials := func(value string) {
		t.Helper()
		if strings.Contains(value, "secret-test") || strings.Contains(value, password) || strings.Contains(value, "password_cipher") {
			t.Fatal("credentials exposed in public output")
		}
	}
	t.Run("manual TOFU registration is transactional and idempotent", func(t *testing.T) {
		host, port, pin, auths, execs := testSSHHost(t, false)
		id := newIntake(host, port, "")
		result, err := call(actor, session, id, fmt.Sprintf("turn:%d:round:1:register", turn))
		if err != nil || !result.Pending || result.ActionID == 0 {
			t.Fatalf("proposal %+v %v", result, err)
		}
		a := getAction(result.ActionID)
		assertNoCredentials(string(a.Payload))
		assertNoCredentials(result.Content)
		var p serverRegisterPayload
		if json.Unmarshal(a.Payload, &p) != nil || p.TrustSource != "first_use" || p.HostKeySHA256 != pin || !p.HasPassword {
			t.Fatalf("payload %+v", p)
		}
		var registered int64
		if err := db.QueryRow(`SELECT registered_server_id FROM v2_ai_server_intake WHERE id=$1`, id).Scan(&registered); err != nil || registered != 0 {
			t.Fatal("proposal performed registration", registered, err)
		}
		dup, err := call(actor, session, id, fmt.Sprintf("turn:%d:round:2:again", turn))
		if err != nil || dup.ActionID != a.ID || !dup.Pending {
			t.Fatal("pending proposal duplicated", dup, err)
		}
		older, err := call(actor, session, id, fmt.Sprintf("turn:%d:round:1:again", turn+1))
		if err != nil || older.ActionID != a.ID || older.Pending {
			t.Fatal("old authorization adopted", older, err)
		}
		done, err := s.ApproveAction(ctx, other, session, a.ID)
		if err != nil || done.Status != "succeeded" || done.ApprovedBy != other {
			t.Fatalf("approve %+v %v", done, err)
		}
		serverID := getServerID(done.Output)
		assertNoCredentials(done.Output)
		var stored string
		if err := db.QueryRow(`SELECT password_cipher FROM v2_ai_server WHERE id=$1`, serverID).Scan(&stored); err != nil || stored != password {
			t.Fatal("encrypted credential not preserved", err)
		}
		if auths.Load() != 0 || execs.Load() != 0 {
			t.Fatal("registration authenticated or executed SSH")
		}
		repeated, err := call(actor, session, id, "later-repeat")
		if err != nil || repeated.Pending || getServerID(repeated.Content) != serverID {
			t.Fatal("registered intake did not reuse server", repeated, err)
		}
		if _, err := s.ApproveAction(ctx, actor, session, a.ID); !errors.Is(err, ErrConflict) {
			t.Fatal("duplicate approval accepted", err)
		}
		collision := newIntake(host, port, "")
		conflict, err := call(actor, session, collision, "different-intake")
		if err != nil || conflict.Pending || !strings.Contains(conflict.Content, `"code":"conflict"`) {
			t.Fatal("existing target overwritten", conflict, err)
		}
	})
	t.Run("provided fingerprint must match without authentication", func(t *testing.T) {
		host, port, pin, auths, _ := testSSHHost(t, false)
		wrong := "SHA256:" + strings.Repeat("A", 43)
		id := newIntake(host, port, wrong)
		result, err := call(actor, session, id, "wrong-pin")
		if err != nil || result.Pending || !strings.Contains(result.Content, "host_key_mismatch") || auths.Load() != 0 {
			t.Fatal(result, err)
		}
		id = newIntake(host, port, pin)
		result, err = call(actor, session, id, "provided-pin")
		if err != nil || !result.Pending || !strings.Contains(string(getAction(result.ActionID).Payload), `"trust_source": "provided"`) {
			t.Fatal(result, err)
		}
		if _, err = s.ApproveAction(ctx, actor, session, result.ActionID); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("scope expiry and stale credentials block registration", func(t *testing.T) {
		host, port, _, _, _ := testSSHHost(t, false)
		id := newIntake(host, port, "")
		for _, scope := range [][2]int64{{other, session}, {actor, otherSession}} {
			if _, err := call(scope[0], scope[1], id, "wrong-scope"); !errors.Is(err, ErrNotFound) {
				t.Fatal("intake escaped scope", err)
			}
		}
		result, err := call(actor, session, id, "stale-intake")
		if err != nil {
			t.Fatal(err)
		}
		replacement, _ := s.encryptSecret("changed-secret")
		if _, err := db.Exec(`UPDATE v2_ai_server_intake SET password_cipher=$2 WHERE id=$1`, id, replacement); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ApproveAction(ctx, actor, session, result.ActionID); !errors.Is(err, ErrConflict) {
			t.Fatal("stale credential snapshot accepted", err)
		}
		if _, err := db.Exec(`UPDATE v2_ai_server_intake SET expires_at=1 WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := call(actor, session, id, "expired-intake"); !errors.Is(err, ErrConflict) {
			t.Fatal("expired intake accepted", err)
		}
	})
	t.Run("concurrent duplicate proposal and approval create only one server", func(t *testing.T) {
		host, port, _, _, _ := testSSHHost(t, false)
		id := newIntake(host, port, "")
		var wg sync.WaitGroup
		results := make([]ToolOutcome, 2)
		errs := make([]error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				results[i], errs[i] = call(actor, session, id, fmt.Sprintf("turn:%d:round:3:concurrent-%d", turn, i))
			}(i)
		}
		wg.Wait()
		for i := range errs {
			if errs[i] != nil {
				t.Fatal(errs[i])
			}
		}
		if results[0].ActionID != results[1].ActionID {
			t.Fatal("duplicate pending actions", results)
		}
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(i int) { defer wg.Done(); _, errs[i] = s.ApproveAction(ctx, actor, session, results[i].ActionID) }(i)
		}
		wg.Wait()
		succeeded := 0
		for _, err := range errs {
			if err == nil {
				succeeded++
			} else if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrBusy) {
				t.Fatal(err)
			}
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM v2_ai_server WHERE host=$1 AND port=$2 AND username='root'`, host, port).Scan(&count); err != nil || succeeded != 1 || count != 1 {
			t.Fatal("duplicate registration", count, succeeded, err)
		}
	})
	t.Run("manual server creation wins without overwritten credentials", func(t *testing.T) {
		host, port, pin, _, _ := testSSHHost(t, false)
		id := newIntake(host, port, "")
		result, err := call(actor, session, id, "manual-race")
		if err != nil {
			t.Fatal(err)
		}
		saved, err := s.SaveServer(ctx, actor, ServerSaveRequest{Name: "manual", Host: host, Port: port, Username: "root", HostKeySHA256: pin, Password: "manual-secret", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ApproveAction(ctx, actor, session, result.ActionID); !errors.Is(err, ErrConflict) {
			t.Fatal("existing manual server replaced", err)
		}
		var cipher string
		if err := db.QueryRow(`SELECT password_cipher FROM v2_ai_server WHERE id=$1`, saved.ID).Scan(&cipher); err != nil {
			t.Fatal(err)
		}
		plain, err := s.decryptSecret(cipher)
		if err != nil || plain != "manual-secret" {
			t.Fatal("manual credential replaced", err)
		}
	})
	t.Run("full access registration and revocation use existing authorization", func(t *testing.T) {
		if _, err := db.Exec(`UPDATE v2_ai_session SET access_mode='full',full_access_by=$2 WHERE id=$1`, session, actor); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE v2_ai_turn SET access_mode='full',status='running',stop_requested=FALSE WHERE id=$1`, turn); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(`UPDATE v2_ai_turn SET status='completed' WHERE id=$1`, turn)
		for _, state := range []string{"enabled", "revoked", "stopped"} {
			host, port, _, _, _ := testSSHHost(t, false)
			id := newIntake(host, port, "")
			result, err := call(actor, session, id, fmt.Sprintf("turn:%d:round:4:%s", turn, state))
			if err != nil {
				t.Fatal(err)
			}
			if state == "revoked" {
				if _, err := db.Exec(`UPDATE v2_ai_session SET access_mode='standard' WHERE id=$1`, session); err != nil {
					t.Fatal(err)
				}
			}
			if state == "stopped" {
				if _, err := db.Exec(`UPDATE v2_ai_turn SET stop_requested=TRUE WHERE id=$1`, turn); err != nil {
					t.Fatal(err)
				}
			}
			done, err := s.automaticActionResult(ctx, actor, session, turn, result)
			if state == "enabled" {
				if err != nil || !strings.Contains(done.Content, `"status":"succeeded"`) {
					t.Fatal(done, err)
				}
				a := getAction(result.ActionID)
				if a.ApprovalMode != "full_access" || a.AuthorizationTurnID != turn {
					t.Fatal("authorization record missing", a)
				}
				getServerID(a.Output)
			} else if !errors.Is(err, ErrForbidden) {
				t.Fatal("revoked registration executed", state, err)
			}
			if _, err := db.Exec(`UPDATE v2_ai_session SET access_mode='full' WHERE id=$1`, session); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE v2_ai_turn SET stop_requested=FALSE WHERE id=$1`, turn); err != nil {
				t.Fatal(err)
			}
		}
	})
}
