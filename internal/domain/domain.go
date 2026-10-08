// Package domain: kiểu dữ liệu nghiệp vụ, khớp HỢP ĐỒNG SCHEMA MongoDB với Portal
// (docs/PLAN_SERVICE_API.md §4, §5.4, §5.4b; Portal đọc trực tiếp các collection này).
//
// Quy ước:
//   - Thời gian lưu UTC.
//   - `date` của stats_* = 00:00:00Z của ngày lịch Asia/Ho_Chi_Minh; `month` = ngày 1 của tháng.
//   - Khoá map by_*: "." → "．" (U+FF0E), rỗng → "(direct)".
package domain

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Trạng thái link (thay cặp cờ is_disabled / is_deleted của MySQL).
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
	StatusDeleted  = "deleted"
)

// Phiên bản API tạo link (links.api_version, clicks.link_api_version).
const (
	APIv1     = "v1"
	APIv2     = "v2"
	APIv3     = "v3"
	APIPortal = "portal"
)

// Link: collection am_shortlink.links.
type Link struct {
	ID            int64      `bson:"_id" json:"id"`
	Code          string     `bson:"code" json:"code"`
	DomainID      int64      `bson:"domain_id" json:"domain_id"`
	LongURL       string     `bson:"long_url" json:"long_url"`
	LongURLHash   string     `bson:"long_url_hash" json:"-"`
	OwnerID       int64      `bson:"owner_id" json:"owner_id"`
	OwnerUsername string     `bson:"owner_username" json:"owner_username"`
	CampaignID    int64      `bson:"campaign_id,omitempty" json:"campaign_id,omitempty"`
	CampaignCode  string     `bson:"campaign_code" json:"campaign_code"`
	CTVRaw        string     `bson:"ctv_raw" json:"-"`
	CTVID         string     `bson:"ctv_id" json:"ctv_id"`
	Status        string     `bson:"status" json:"status"`
	IsCustom      bool       `bson:"is_custom" json:"is_custom"`
	IsAPI         bool       `bson:"is_api" json:"is_api"`
	APIVersion    string     `bson:"api_version" json:"api_version"`
	Prefix        string     `bson:"prefix" json:"prefix"`
	DestHost      string     `bson:"dest_host" json:"dest_host"`
	SecretKey     string     `bson:"secret_key,omitempty" json:"-"`
	ExpiresAt     *time.Time `bson:"expires_at" json:"expires_at"`
	Clicks        int64      `bson:"clicks" json:"clicks"`
	IP            string     `bson:"ip" json:"-"`
	CreatedAt     time.Time  `bson:"created_at" json:"created_at"`
	UpdatedAt     time.Time  `bson:"updated_at" json:"updated_at"`
}

// User: collection am_shortlink.users. API key KHÔNG nằm ở đây (xem APIKey).
type User struct {
	ID              int64     `bson:"_id"`
	Username        string    `bson:"username"`
	Email           string    `bson:"email"`
	PasswordHash    string    `bson:"password_hash"`
	Role            string    `bson:"role"`
	Active          bool      `bson:"active"`
	APIActive       bool      `bson:"api_active"`
	APIQuota        int       `bson:"api_quota"`
	Prefix          string    `bson:"prefix"`
	RandomKeyLength int       `bson:"random_key_length"`
	IsExpires       bool      `bson:"is_expires"`
	ExpiresValue    int       `bson:"expires_value"`
	PortalAccess    bool      `bson:"portal_access"`
	ViewerAccounts  []string  `bson:"viewer_accounts"`
	CreatedAt       time.Time `bson:"created_at"`
}

func (u *User) IsAdmin() bool { return u.Role == "admin" }

// APIKey: collection am_shortlink.api_keys — nguồn duy nhất của API key (chỉ lưu SHA-256).
type APIKey struct {
	ID         bson.ObjectID `bson:"_id,omitempty"`
	UserID     int64         `bson:"user_id"`
	KeyHash    string        `bson:"key_hash"`
	Active     bool          `bson:"active"`
	CreatedAt  time.Time     `bson:"created_at"`
	RotatedAt  *time.Time    `bson:"rotated_at,omitempty"`
	LastUsedAt *time.Time    `bson:"last_used_at,omitempty"`
}

// Campaign: collection am_shortlink.campaigns. created_by = users._id (giữ như MySQL);
// dữ liệu seed cũ có thể là username → đọc bằng RawValue.
type Campaign struct {
	ID        int64         `bson:"_id"`
	Name      string        `bson:"name"`
	Code      string        `bson:"code"`
	CreatedBy bson.RawValue `bson:"created_by"`
	CreatedAt time.Time     `bson:"created_at"`
	UpdatedAt time.Time     `bson:"updated_at"`
}

// OwnedBy: campaign thuộc user (so created_by với id, chấp nhận cả dạng username).
func (c *Campaign) OwnedBy(u *User) bool {
	if v, ok := c.CreatedBy.AsInt64OK(); ok {
		return v == u.ID
	}
	if s, ok := c.CreatedBy.StringValueOK(); ok {
		return s == u.Username
	}
	return false
}

// CreatedByID: id người tạo (0 nếu không phải số).
func (c *Campaign) CreatedByID() int64 {
	v, _ := c.CreatedBy.AsInt64OK()
	return v
}

// Template: collection am_shortlink.templates (giữ tên trường MySQL vì API v2 trả nguyên).
type Template struct {
	ID             int64     `bson:"_id"`
	TemplateName   string    `bson:"template_name"`
	TemplateURL    string    `bson:"template_url"`
	TemplateImages string    `bson:"template_images"`
	Status         int       `bson:"status"`
	CreatedAt      time.Time `bson:"created_at"`
}

// Domain: collection am_shortlink.domains.
type Domain struct {
	ID         int64  `bson:"_id"`
	DomainName string `bson:"domain_name"`
	IsActive   bool   `bson:"is_active"`
}

// Prefix: collection am_shortlink.prefixes (_id = "sale", "lm"...).
type Prefix struct {
	ID          string `bson:"_id"`
	IsDefault   bool   `bson:"is_default"`
	IsDefaultV3 bool   `bson:"is_default_v3"`
	Active      bool   `bson:"active"`
	Description string `bson:"description"`
}

// Param: một tham số query của long_url (đã chuẩn hoá theo param_registry).
type Param struct {
	Key   string `json:"k"`
	Value string `json:"v"`
	Raw   string `json:"r,omitempty"`
}
