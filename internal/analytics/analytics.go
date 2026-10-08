// Package analytics: nhận diện thiết bị / hệ điều hành / trình duyệt / in-app / bot từ User-Agent
// và nhóm nguồn từ referer (§5.5 R6b). Luật đơn giản, không gọi dịch vụ ngoài, chạy trong consumer.
package analytics

import (
	"net/url"
	"strings"
	"time"
)

var VN = loadVN()

func loadVN() *time.Location {
	l, err := time.LoadLocation("Asia/Ho_Chi_Minh")
	if err != nil {
		return time.FixedZone("ICT", 7*3600) // Windows thiếu tzdata
	}
	return l
}

// DateOf: 00:00:00Z của ngày lịch VN chứa t (quy ước `date` của stats_*).
func DateOf(t time.Time) time.Time {
	t = t.In(VN)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// MonthOf: ngày 1 của tháng (UTC) cho một `date`.
func MonthOf(d time.Time) time.Time { return time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC) }

type UA struct {
	Device  string // mobile | desktop | tablet | bot | unknown
	OS      string
	Browser string
	InApp   string // zalo | facebook | tiktok | instagram | ""
	IsBot   bool
}

var botMarkers = []string{
	"bot", "crawler", "spider", "slurp", "facebookexternalhit", "facebot", "zalo link preview",
	"preview", "curl", "wget", "python-requests", "go-http-client", "httpclient", "okhttp",
	"headlesschrome", "lighthouse", "pingdom", "uptime", "monitor", "whatsapp", "telegrambot",
	"skypeuripreview", "discordbot", "slackbot", "linkedinbot", "twitterbot", "applebot", "bingpreview",
}

// ParseUA phân tích User-Agent.
func ParseUA(ua string) UA {
	s := strings.ToLower(ua)
	if strings.TrimSpace(s) == "" {
		return UA{Device: "unknown", OS: "unknown", Browser: "unknown"}
	}
	for _, m := range botMarkers {
		if strings.Contains(s, m) {
			return UA{Device: "bot", OS: "unknown", Browser: botName(s), IsBot: true}
		}
	}
	r := UA{OS: "unknown", Browser: "unknown", Device: "unknown"}

	switch {
	case strings.Contains(s, "ipad"):
		r.OS, r.Device = "iOS", "tablet"
	case strings.Contains(s, "iphone") || strings.Contains(s, "ipod"):
		r.OS, r.Device = "iOS", "mobile"
	case strings.Contains(s, "android"):
		r.OS = "Android"
		if strings.Contains(s, "mobile") {
			r.Device = "mobile"
		} else {
			r.Device = "tablet"
		}
	case strings.Contains(s, "windows phone"):
		r.OS, r.Device = "Windows Phone", "mobile"
	case strings.Contains(s, "windows"):
		r.OS, r.Device = "Windows", "desktop"
	case strings.Contains(s, "macintosh") || strings.Contains(s, "mac os x"):
		r.OS, r.Device = "macOS", "desktop"
	case strings.Contains(s, "cros"):
		r.OS, r.Device = "ChromeOS", "desktop"
	case strings.Contains(s, "linux"):
		r.OS, r.Device = "Linux", "desktop"
	}

	switch {
	case strings.Contains(s, "zalo"):
		r.InApp, r.Browser = "zalo", "Zalo in-app"
	case strings.Contains(s, "fban") || strings.Contains(s, "fbav") || strings.Contains(s, "fb_iab") || strings.Contains(s, "fbios"):
		r.InApp, r.Browser = "facebook", "Facebook in-app"
	case strings.Contains(s, "musical_ly") || strings.Contains(s, "bytedancewebview") || strings.Contains(s, "tiktok"):
		r.InApp, r.Browser = "tiktok", "TikTok in-app"
	case strings.Contains(s, "instagram"):
		r.InApp, r.Browser = "instagram", "Instagram in-app"
	case strings.Contains(s, "coc_coc") || strings.Contains(s, "coccoc"):
		r.Browser = "Cốc Cốc"
	case strings.Contains(s, "edg/") || strings.Contains(s, "edge/"):
		r.Browser = "Edge"
	case strings.Contains(s, "samsungbrowser"):
		r.Browser = "Samsung Internet"
	case strings.Contains(s, "opr/") || strings.Contains(s, "opera"):
		r.Browser = "Opera"
	case strings.Contains(s, "firefox") || strings.Contains(s, "fxios"):
		r.Browser = "Firefox"
	case strings.Contains(s, "crios") || strings.Contains(s, "chrome"):
		r.Browser = "Chrome"
	case strings.Contains(s, "safari"):
		r.Browser = "Safari"
	}
	return r
}

func botName(s string) string {
	for _, n := range []struct{ marker, name string }{
		{"googlebot", "Googlebot"}, {"facebookexternalhit", "facebookexternalhit"}, {"zalo", "Zalo link preview"},
		{"bingbot", "Bingbot"}, {"curl", "curl"}, {"wget", "wget"}, {"python", "python"}, {"telegram", "Telegram"},
		{"whatsapp", "WhatsApp"}, {"twitterbot", "Twitterbot"}, {"slackbot", "Slackbot"}, {"applebot", "Applebot"},
	} {
		if strings.Contains(s, n.marker) {
			return n.name
		}
	}
	return "bot"
}

// RefererHost lấy host từ referer.
func RefererHost(ref string) string {
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// SourceGroup nhóm nguồn theo referer_host + in-app (P-D7; bảng có thể chuyển sang cấu hình).
func SourceGroup(refHost, inApp string) string {
	switch inApp {
	case "zalo", "facebook", "tiktok", "instagram":
		return inApp
	}
	h := refHost
	switch {
	case h == "":
		return "direct"
	case hasDomain(h, "zalo.me", "zaloapp.com", "zalo.vn"):
		return "zalo"
	case hasDomain(h, "facebook.com", "fb.com", "messenger.com", "fb.me"):
		return "facebook"
	case hasDomain(h, "tiktok.com"):
		return "tiktok"
	case hasDomain(h, "instagram.com"):
		return "instagram"
	case strings.HasPrefix(h, "google.") || strings.Contains(h, ".google.") || hasDomain(h, "googleusercontent.com"):
		return "google"
	case hasDomain(h, "youtube.com", "youtu.be"):
		return "youtube"
	default:
		return "other"
	}
}

func hasDomain(h string, ds ...string) bool {
	for _, d := range ds {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}
