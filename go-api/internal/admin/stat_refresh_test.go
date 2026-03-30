package admin

import (
	"testing"
	"time"
)

func TestLegacyStatWindowsIncludeYesterdayAndToday(t *testing.T) {
	now := time.Unix(1_711_699_600, 0).UTC()

	windows := legacyStatWindows(now)
	if len(windows) != 2 {
		t.Fatalf("expected two stat windows, got %#v", windows)
	}
	if windows[0].recordAt != 1711584000 || windows[0].startAt != 1711584000 || windows[0].endAt != 1711670400 {
		t.Fatalf("unexpected yesterday window: %#v", windows[0])
	}
	if windows[1].recordAt != 1711670400 || windows[1].startAt != 1711670400 || windows[1].endAt != now.Unix() {
		t.Fatalf("unexpected today window: %#v", windows[1])
	}
}
