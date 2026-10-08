// Package events: sự kiện giữa redirect/api và consumer.
//
// Redirect / API chỉ PHÁT sự kiện (không ghi thống kê đồng bộ). Consumer nhận và ghi
// clicks + stats_* + link_params. Sink:
//   - kafka : franz-go, topic click / link (production).
//   - direct: xử lý ngay trong tiến trình qua hàng đợi nội bộ (dev, không cần Kafka).
//   - log   : bỏ qua (chỉ log) — dùng khi test tải redirect.
package events

import (
	"context"
	"time"

	"am-shortlink-service/internal/domain"
)

// LinkSnapshot: thông tin link đi kèm click — consumer không phải tra lại links.
type LinkSnapshot struct {
	ID         int64     `json:"id"`
	Code       string    `json:"code"`
	LongURL    string    `json:"long_url"`
	Owner      string    `json:"owner"`
	Campaign   string    `json:"campaign_code"`
	CTVID      string    `json:"ctv_id"`
	Prefix     string    `json:"prefix"`
	APIVersion string    `json:"api_version"`
	IsCustom   bool      `json:"is_custom"`
	DestHost   string    `json:"dest_host"`
	CreatedAt  time.Time `json:"created_at"`
}

func SnapshotOf(l *domain.Link) LinkSnapshot {
	return LinkSnapshot{
		ID: l.ID, Code: l.Code, LongURL: l.LongURL, Owner: l.OwnerUsername, Campaign: l.CampaignCode,
		CTVID: l.CTVID, Prefix: l.Prefix, APIVersion: l.APIVersion, IsCustom: l.IsCustom,
		DestHost: l.DestHost, CreatedAt: l.CreatedAt,
	}
}

// ClickEvent: một lượt redirect thành công.
type ClickEvent struct {
	EventID      string       `json:"event_id"`
	TS           time.Time    `json:"ts"`
	IP           string       `json:"ip"`
	UserAgent    string       `json:"user_agent"`
	Referer      string       `json:"referer"`
	AccessPrefix string       `json:"access_prefix"`
	Link         LinkSnapshot `json:"link"`
}

// Loại sự kiện link.
const (
	LinkCreated = "created"
	LinkUpdated = "updated" // đổi long_url / campaign
	LinkDeleted = "deleted"
	LinkRestore = "restored"
)

// LinkEvent: link được tạo / sửa / xoá — consumer cập nhật link_params, param_values, new_links.
type LinkEvent struct {
	EventID string       `json:"event_id"`
	Type    string       `json:"type"`
	TS      time.Time    `json:"ts"`
	Link    LinkSnapshot `json:"link"`
}

// Publisher phát sự kiện. Không được chặn đường nóng: lỗi / đầy hàng đợi → bỏ + đếm.
type Publisher interface {
	PublishClick(ctx context.Context, e ClickEvent)
	PublishLink(ctx context.Context, e LinkEvent)
	Close(ctx context.Context) error
}

// Handler xử lý sự kiện (consumer / sink direct).
type Handler interface {
	HandleClicks(ctx context.Context, es []ClickEvent) error
	HandleLink(ctx context.Context, e LinkEvent) error
}
