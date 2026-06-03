package user

import (
	"context"
	"strings"
	"testing"

	"forest/go-api/internal/config"
)

func TestBuildSubscribeURLUsesAppURLWhenSubscribeURLMissing(t *testing.T) {
	service := NewDBService(config.Config{
		AppURL:        "https://panel.example.com",
		SubscribePath: "/api/v1/client/subscribe",
	}, nil)

	got, err := service.buildSubscribeURL(context.Background(), 1, "token-1")
	if err != nil {
		t.Fatalf("build subscribe url: %v", err)
	}
	want := "https://panel.example.com/api/v1/client/subscribe?token=token-1"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestBuildSubscribeURLSupportsMultipleBaseURLs(t *testing.T) {
	service := NewDBService(config.Config{
		SubscribeURL:  "https://sub1.example.com, https://sub2.example.com",
		SubscribePath: "/sub",
	}, nil)

	got, err := service.buildSubscribeURL(context.Background(), 1, "token-1")
	if err != nil {
		t.Fatalf("build subscribe url: %v", err)
	}
	if strings.Contains(got, ",") {
		t.Fatalf("expected subscribe url to use one base url, got %q", got)
	}
	if !strings.HasPrefix(got, "https://sub1.example.com/sub?token=") && !strings.HasPrefix(got, "https://sub2.example.com/sub?token=") {
		t.Fatalf("unexpected subscribe url %q", got)
	}
}

func TestBuildSubscribeURLTrimsTrailingSlashForQueryToken(t *testing.T) {
	service := NewDBService(config.Config{
		AppURL:        "https://panel.example.com",
		SubscribePath: "/forest-sub/",
	}, nil)

	got, err := service.buildSubscribeURL(context.Background(), 1, "token-1")
	if err != nil {
		t.Fatalf("build subscribe url: %v", err)
	}
	want := "https://panel.example.com/forest-sub?token=token-1"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestBuildSubscribeURLCanPutTokenInPath(t *testing.T) {
	service := NewDBService(config.Config{
		AppURL:               "https://panel.example.com",
		SubscribePath:        "/forest-sub",
		SubscribeTokenInPath: true,
	}, nil)

	got, err := service.buildSubscribeURL(context.Background(), 1, "token-1")
	if err != nil {
		t.Fatalf("build subscribe url: %v", err)
	}
	want := "https://panel.example.com/forest-sub/token-1"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
	if strings.Contains(got, "?token=") {
		t.Fatalf("expected subscribe url to omit token query, got %q", got)
	}
}
