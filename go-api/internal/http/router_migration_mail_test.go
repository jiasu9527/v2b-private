package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"forest/go-api/internal/usermigration"
)

func migrationMailRoute(t *testing.T, req usermigration.MailRequest) userMigrationRoute {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return userMigrationRoute{action: "mail/send", method: http.MethodPost, body: string(body)}
}

func validMigrationMailRequest() usermigration.MailRequest {
	return usermigration.MailRequest{PreviewID: migrationTestPreviewID, RequestID: strings.Repeat("a", 64), Subject: "迁移通知", Content: "请登录新站获取订阅。", Confirm: true}
}

func TestMigrationMailExplicitSubmitAndReadOnlyStatus(t *testing.T) {
	req := validMigrationMailRequest()
	service := &fakeUserMigrationService{mailTask: usermigration.MailTask{RequestID: req.RequestID, PreviewID: req.PreviewID, Status: "queued", Recipients: 2, IntervalSeconds: 5}, mailInfo: usermigration.MailInfo{PreviewID: req.PreviewID, Imported: 2, Recipients: 2, TrackingAvailable: true, Configured: true, QueueAvailable: true, IntervalSeconds: 5}}
	router := newUserMigrationRouter(userMigrationAdminSession(), service)
	rec := requestUserMigration(t, router, userMigrationRoute{action: "mail/status", method: http.MethodGet, query: "?preview_id=" + req.PreviewID}, "header-token")
	if rec.Code != 200 || len(service.calls) != 1 || service.calls[0] != "mail/status" {
		t.Fatalf("status query sends mail: %d %v", rec.Code, service.calls)
	}
	var info struct{ Data usermigration.MailInfo }
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil || info.Data.IntervalSeconds != 5 || !info.Data.TrackingAvailable || info.Data.Task != nil {
		t.Fatalf("wrong status: %s err=%v", rec.Body.String(), err)
	}
	req.Subject = "  迁移通知  "
	req.Content = "  请登录新站获取订阅。 \n"
	rec = requestUserMigration(t, router, migrationMailRoute(t, req), "header-token")
	if rec.Code != 200 || service.actorID != 41 || service.mailRequest.Subject != "迁移通知" || service.mailRequest.Content != "请登录新站获取订阅。" || !service.mailRequest.Confirm {
		t.Fatalf("wrong submission: %d %+v", rec.Code, service.mailRequest)
	}
	var task struct{ Data usermigration.MailTask }
	if err := json.Unmarshal(rec.Body.Bytes(), &task); err != nil || task.Data.RequestID != req.RequestID || task.Data.Status != "queued" || task.Data.Sent != 0 {
		t.Fatalf("queue response presented as sent: %s err=%v", rec.Body.String(), err)
	}
}

func TestMigrationMailRejectsInvalidContentWithoutCallingService(t *testing.T) {
	tests := []struct {
		name   string
		change func(*usermigration.MailRequest)
	}{
		{"missing confirm", func(r *usermigration.MailRequest) { r.Confirm = false }},
		{"preview id", func(r *usermigration.MailRequest) { r.PreviewID = "short" }},
		{"missing key", func(r *usermigration.MailRequest) { r.RequestID = "" }},
		{"uppercase key", func(r *usermigration.MailRequest) { r.RequestID = strings.Repeat("A", 64) }},
		{"subject empty", func(r *usermigration.MailRequest) { r.Subject = " \n " }},
		{"subject too long", func(r *usermigration.MailRequest) { r.Subject = strings.Repeat("中", 201) }},
		{"subject newline", func(r *usermigration.MailRequest) { r.Subject = "title\r\nBcc: another@example.test" }},
		{"subject null", func(r *usermigration.MailRequest) { r.Subject = "title\x00suffix" }},
		{"body empty", func(r *usermigration.MailRequest) { r.Content = " \n " }},
		{"body too long", func(r *usermigration.MailRequest) { r.Content = strings.Repeat("中", 33334) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := validMigrationMailRequest()
			tc.change(&req)
			service := &fakeUserMigrationService{}
			rec := requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), service), migrationMailRoute(t, req), "header-token")
			if rec.Code != 400 || len(service.calls) != 0 {
				t.Fatalf("invalid mail accepted: %d calls=%v", rec.Code, service.calls)
			}
		})
	}
}

func TestMigrationMailRejectsRecipientOrOwnerInjectionAndOversizeBody(t *testing.T) {
	route := migrationMailRoute(t, validMigrationMailRequest())
	for _, field := range []string{"emails", "recipients", "actor_id", "user_id", "filter", "source"} {
		t.Run(field, func(t *testing.T) {
			service := &fakeUserMigrationService{}
			candidate := route
			candidate.body = strings.TrimSuffix(route.body, "}") + `,"` + field + `":"` + migrationTestSecret + `"}`
			rec := requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), service), candidate, "header-token")
			if rec.Code != 400 || len(service.calls) != 0 || strings.Contains(rec.Body.String(), migrationTestSecret) {
				t.Fatalf("unsafe input accepted/reflected: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
	for _, body := range []string{route.body + `{}`, route.body + strings.Repeat(" ", 1<<20)} {
		service := &fakeUserMigrationService{}
		candidate := route
		candidate.body = body
		rec := requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), service), candidate, "header-token")
		if rec.Code != 400 || len(service.calls) != 0 {
			t.Fatalf("oversize/trailing body accepted: %d", rec.Code)
		}
	}
	// A maximum valid body can exceed the normal 64 KiB migration JSON limit.
	req := validMigrationMailRequest()
	req.Subject = strings.Repeat("中", 200)
	req.Content = strings.Repeat("<", 100000)
	service := &fakeUserMigrationService{}
	rec := requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), service), migrationMailRoute(t, req), "header-token")
	if rec.Code != 200 || len(service.mailRequest.Content) != 100000 {
		t.Fatalf("maximum escaped JSON rejected: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMigrationMailErrorCodes(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   string
		status int
	}{
		{usermigration.ErrMailTrackingUnavailable, "mail_tracking_unavailable", 409},
		{usermigration.ErrMailUnavailable, "mail_unavailable", 503},
		{usermigration.ErrMailQueueUnavailable, "mail_queue_unavailable", 503},
		{usermigration.ErrMailNoRecipients, "mail_no_recipients", 409},
		{usermigration.ErrMailConflict, "mail_conflict", 409},
		{usermigration.ErrMailBusy, "mail_busy", 409},
	} {
		t.Run(tc.code, func(t *testing.T) {
			service := &fakeUserMigrationService{err: tc.err}
			rec := requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), service), migrationMailRoute(t, validMigrationMailRequest()), "header-token")
			var out struct{ Code string }
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != tc.status || out.Code != tc.code {
				t.Fatalf("wrong error: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}
