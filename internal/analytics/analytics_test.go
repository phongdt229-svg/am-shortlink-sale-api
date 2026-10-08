package analytics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestParseUA(t *testing.T) {
	iphoneZalo := "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 Zalo iOS/489"
	u := ParseUA(iphoneZalo)
	assert.Equal(t, "mobile", u.Device)
	assert.Equal(t, "iOS", u.OS)
	assert.Equal(t, "zalo", u.InApp)
	assert.False(t, u.IsBot)

	win := ParseUA("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36")
	assert.Equal(t, "desktop", win.Device)
	assert.Equal(t, "Chrome", win.Browser)

	bot := ParseUA("facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php)")
	assert.True(t, bot.IsBot)
	assert.Equal(t, "bot", bot.Device)

	assert.True(t, ParseUA("curl/8.0").IsBot)
	assert.Equal(t, "unknown", ParseUA("").Device)
}

func TestSourceGroup(t *testing.T) {
	assert.Equal(t, "zalo", SourceGroup("zalo.me", ""))
	assert.Equal(t, "facebook", SourceGroup("l.facebook.com", ""))
	assert.Equal(t, "google", SourceGroup("www.google.com.vn", ""))
	assert.Equal(t, "direct", SourceGroup("", ""))
	assert.Equal(t, "tiktok", SourceGroup("", "tiktok"))
	assert.Equal(t, "other", SourceGroup("vnexpress.net", ""))
}

func TestDateOfUsesVietnamDay(t *testing.T) {
	// 2026-10-07 18:30 UTC = 2026-10-08 01:30 giờ VN → ngày 08
	ts := time.Date(2026, 10, 7, 18, 30, 0, 0, time.UTC)
	assert.Equal(t, time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC), DateOf(ts))
	assert.Equal(t, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), MonthOf(DateOf(ts)))
}
