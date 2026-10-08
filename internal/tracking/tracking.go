// Package tracking: gửi click sang TrackingApi (Sale Platform) và FMI cho link của các user cấu hình.
//
// Hệ cũ gọi ĐỒNG BỘ trên redirect (timeout 30s) và hard-code user `lehongan` (D2). Ở đây chạy
// bất đồng bộ trong consumer qua worker pool; hàng đợi đầy → bỏ (không chặn thống kê).
package tracking

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"am-shortlink-service/internal/config"
	"am-shortlink-service/internal/events"
)

type Dispatcher struct {
	cfg      config.Tracking
	log      *slog.Logger
	http     *http.Client
	users    map[string]bool
	fmiUsers map[string]bool
	q        chan events.ClickEvent
	wg       sync.WaitGroup

	tokMu  sync.Mutex
	token  string
	tokExp time.Time
}

// New trả nil nếu không cấu hình user nào.
func New(cfg config.Tracking, log *slog.Logger) *Dispatcher {
	if len(cfg.Users) == 0 && len(cfg.FMIUsers) == 0 {
		return nil
	}
	d := &Dispatcher{
		cfg: cfg, log: log, http: &http.Client{Timeout: cfg.Timeout},
		users: set(cfg.Users), fmiUsers: set(cfg.FMIUsers), q: make(chan events.ClickEvent, 5000),
	}
	for i := 0; i < 8; i++ {
		d.wg.Add(1)
		go d.worker()
	}
	return d
}

func set(xs []string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		if x = strings.TrimSpace(x); x != "" {
			m[x] = true
		}
	}
	return m
}

func (d *Dispatcher) Track(e events.ClickEvent) {
	if !d.users[e.Link.Owner] && !d.fmiUsers[e.Link.Owner] {
		return
	}
	select {
	case d.q <- e:
	default:
		d.log.Warn("tracking queue full, dropping", "code", e.Link.Code)
	}
}

func (d *Dispatcher) Close() {
	close(d.q)
	d.wg.Wait()
}

func (d *Dispatcher) worker() {
	defer d.wg.Done()
	for e := range d.q {
		ctx, cancel := context.WithTimeout(context.Background(), d.cfg.Timeout)
		if d.users[e.Link.Owner] && d.cfg.BaseURL != "" {
			if err := d.sendTracking(ctx, e); err != nil {
				d.log.Warn("tracking api", "err", err, "code", e.Link.Code)
			}
		}
		if d.fmiUsers[e.Link.Owner] && d.cfg.FMIBaseURL != "" {
			if err := d.sendFMI(ctx, e); err != nil {
				d.log.Warn("fmi api", "err", err, "code", e.Link.Code)
			}
		}
		cancel()
	}
}

// sendTracking: POST api/v1.0/customer/create-tracking (payload giống LinkController::tracking cũ).
func (d *Dispatcher) sendTracking(ctx context.Context, e events.ClickEvent) error {
	u, err := url.Parse(e.Link.LongURL)
	if err != nil {
		return err
	}
	q := u.Query()
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	body := map[string]any{
		"GuestId":      q.Get("guestid"),
		"CustomerInfo": map[string]any{"FullName": q.Get("fullname"), "PhoneNumber": q.Get("phonenumber")},
		"ServiceInfo":  []any{},
		"CampInfo":     map[string]any{"CampSource": q.Get("utm_source"), "CampMedium": q.Get("utm_medium"), "CampName": q.Get("utm_campaign")},
		"LocationInfo": map[string]any{"LocationId": atoi(q.Get("locationid")), "DistrictId": atoi(q.Get("districtid")), "WardId": atoi(q.Get("wardid")), "StreetId": 0},
		"Actions": []any{map[string]any{
			"PageType": " ShortLink", "Description": "Click vào ShortLink", "SessionId": "",
			"Url": e.Link.LongURL, "SpendTime": 1000, "ActionTime": e.TS.UnixMilli(), "ActionType": "Click",
		}},
	}
	tok, err := d.accessToken(ctx)
	if err != nil {
		return err
	}
	resp, err := d.postJSON(ctx, strings.TrimRight(d.cfg.BaseURL, "/")+"/api/v1.0/customer/create-tracking", body,
		map[string]string{"Authorization": "Bearer " + tok})
	if err != nil {
		return err
	}
	var r struct {
		Code int `json:"Code"`
	}
	if err := json.Unmarshal(resp, &r); err != nil || r.Code != 200 {
		return fmt.Errorf("tracking api code=%d", r.Code)
	}
	return nil
}

// accessToken: client_credentials (basic auth), cache tới khi gần hết hạn.
func (d *Dispatcher) accessToken(ctx context.Context) (string, error) {
	d.tokMu.Lock()
	defer d.tokMu.Unlock()
	if d.token != "" && time.Now().Before(d.tokExp) {
		return d.token, nil
	}
	b, _ := json.Marshal(map[string]string{"GrantType": "client_credentials"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(d.cfg.AuthURL, "/")+"/auth/api/v1/authentication/token", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(d.cfg.ClientID, d.cfg.ClientSecret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r struct {
		Code int `json:"Code"`
		Data struct {
			AccessToken         string  `json:"AccessToken"`
			AccessTokenLifeTime float64 `json:"AccessTokenLifeTime"` // phút
		} `json:"Data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil || r.Code != 200 || r.Data.AccessToken == "" {
		return "", fmt.Errorf("lấy access token thất bại (http %d)", resp.StatusCode)
	}
	life := time.Duration(r.Data.AccessTokenLifeTime) * time.Minute
	if life <= 2*time.Minute {
		life = 5 * time.Minute
	}
	d.token, d.tokExp = r.Data.AccessToken, time.Now().Add(life-time.Minute)
	return d.token, nil
}

// sendFMI: POST /openapi/activity-event/api/v1/events/shortlink {"FullUrl": ...}.
func (d *Dispatcher) sendFMI(ctx context.Context, e events.ClickEvent) error {
	_, err := d.postJSON(ctx, strings.TrimRight(d.cfg.FMIBaseURL, "/")+"/openapi/activity-event/api/v1/events/shortlink",
		map[string]string{"FullUrl": e.Link.LongURL},
		map[string]string{"X-App-Id": "ShortLink", "X-Api-Key": d.cfg.FMIAPIKey})
	return err
}

func (d *Dispatcher) postJSON(ctx context.Context, u string, body any, headers map[string]string) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("http %d", resp.StatusCode)
	}
	return out, nil
}
