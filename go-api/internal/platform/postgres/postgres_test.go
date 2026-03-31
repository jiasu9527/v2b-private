package postgres

import (
	"testing"
	"time"
)

func TestDefaultPoolSettings(t *testing.T) {
	settings := defaultPoolSettings()

	if settings.MaxOpenConns != 64 {
		t.Fatalf("expected max open conns 64, got %d", settings.MaxOpenConns)
	}
	if settings.MaxIdleConns != 16 {
		t.Fatalf("expected max idle conns 16, got %d", settings.MaxIdleConns)
	}
	if settings.ConnMaxLifetime != 30*time.Minute {
		t.Fatalf("expected conn max lifetime 30m, got %s", settings.ConnMaxLifetime)
	}
	if settings.ConnMaxIdleTime != 5*time.Minute {
		t.Fatalf("expected conn max idle time 5m, got %s", settings.ConnMaxIdleTime)
	}
}
