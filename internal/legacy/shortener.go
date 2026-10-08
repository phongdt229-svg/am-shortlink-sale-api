package legacy

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"

	"am-shortlink-service/internal/analytics"
	"am-shortlink-service/internal/config"
	"am-shortlink-service/internal/domain"
	"am-shortlink-service/internal/events"
	"am-shortlink-service/internal/linkcache"
	"am-shortlink-service/internal/shortcode"
	"am-shortlink-service/internal/store"
	"am-shortlink-service/internal/urlparams"
)

const maxLongURL = 65535

// Shortener: nghiệp vụ tạo / sửa / xoá link dùng chung cho v1/v2/v3.
type Shortener struct {
	st    *store.Store
	links *linkcache.Cache
	pub   events.Publisher
	quota *quota
	cfg   config.API
	salt  string
}

// FormatLink: <APP_PROTOCOL><APP_HOST>/<prefix>/<code>.
func (s *Shortener) FormatLink(code, prefix string) string {
	if prefix == "" {
		prefix = s.cfg.DefaultPrefix
	}
	return s.cfg.AppProtocol + s.cfg.AppHost + "/" + prefix + "/" + code
}

// PrefixV3: users.prefix → DEFAULT_PREFIX_V3 → DEFAULT_PREFIX (thứ tự của ShortenController::appAddress).
func (s *Shortener) PrefixV3(u *domain.User) string {
	if p := strings.Trim(strings.TrimSpace(u.Prefix), "/"); p != "" {
		return p
	}
	if s.cfg.DefaultPrefixV3 != "" {
		return s.cfg.DefaultPrefixV3
	}
	return s.cfg.DefaultPrefix
}

func (s *Shortener) keyLen(u *domain.User) int {
	if u != nil && u.RandomKeyLength > 0 {
		return u.RandomKeyLength
	}
	return s.cfg.RandomKeyLength
}

// newLink dựng link mới (chưa ghi).
func (s *Shortener) newLink(u *domain.User, longURL, code string, custom bool, apiVersion, prefix, campaign string, campaignID int64, ip string, domainID int64) *domain.Link {
	p := urlparams.Parse(longURL, nil, s.salt)
	l := &domain.Link{
		Code: code, DomainID: domainID, LongURL: longURL, OwnerID: u.ID, OwnerUsername: u.Username,
		CampaignCode: campaign, CampaignID: campaignID, CTVRaw: p.CTVRaw, CTVID: p.CTVID,
		Status: domain.StatusActive, IsCustom: custom, IsAPI: true, APIVersion: apiVersion, Prefix: prefix,
		DestHost: p.DestHost, IP: ip,
	}
	if u.IsExpires {
		t := time.Now().In(analytics.VN).AddDate(0, 0, u.ExpiresValue)
		d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		l.ExpiresAt = &d
	}
	return l
}

// insertRandom ghi link với mã ngẫu nhiên, thử lại khi trùng (insert-and-retry, không đọc trước).
func (s *Shortener) insertRandom(ctx context.Context, l *domain.Link, n int) error {
	for i := 0; i < 8; i++ {
		l.Code = shortcode.Random(n)
		err := s.st.InsertLink(ctx, l)
		if err == nil {
			return nil
		}
		if !store.IsDuplicate(err) {
			return err
		}
		if i >= 4 {
			n++ // không gian mã gần cạn → tăng độ dài
		}
	}
	return errData("Không sinh đủ mã rút gọn, vui lòng thử lại với số lượng nhỏ hơn.", nullEnding())
}

func (s *Shortener) created(ctx context.Context, u *domain.User, ls ...*domain.Link) {
	codes := make([]string, 0, len(ls))
	for _, l := range ls {
		codes = append(codes, l.Code)
		s.pub.PublishLink(ctx, events.LinkEvent{EventID: uuid.NewString(), Type: events.LinkCreated, TS: time.Now().UTC(), Link: events.SnapshotOf(l)})
	}
	s.links.Invalidate(ctx, codes...) // xoá negative cache của mã vừa tạo
	s.quota.Record(ctx, u, len(ls))
}

func (s *Shortener) changed(ctx context.Context, typ string, l *domain.Link) {
	s.pub.PublishLink(ctx, events.LinkEvent{EventID: uuid.NewString(), Type: typ, TS: time.Now().UTC(), Link: events.SnapshotOf(l)})
	s.links.Invalidate(ctx, l.Code)
}

func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// ---------- v1 ----------

type ShortenResult struct {
	ShortURL string
	Ending   string
	IsNew    bool
	LongURL  string
	// existingByLongURL: v1 trả dạng {short_url, long_url, ending: <short_url đầy đủ>, is_new: false}
	ExistingV1 bool
}

// ShortenV1: /api/v1/shorten (LinkFactory::createLinkApi).
func (s *Shortener) ShortenV1(ctx context.Context, u *domain.User, longURL, domainParam, custom, ip string) (*ShortenResult, error) {
	longURL = strings.TrimSpace(longURL)
	// 1. link không custom đã có của user (mọi trạng thái — longLinkExists cũ không lọc đã xoá) → trả lại
	if l, err := s.st.OwnerNonCustomByLongURL(ctx, u.Username, longURL); err == nil {
		su := s.FormatLink(l.Code, s.cfg.DefaultPrefix)
		return &ShortenResult{ShortURL: su, LongURL: l.LongURL, Ending: su, ExistingV1: true}, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	// 2. kiểm domain long_url
	host := hostOf(longURL)
	if host == "fpt.vn" && strings.Contains(longURL, "fpt.vn/shop") {
		return nil, errData("Đường link dài không hợp lệ. [fpt.vn/shop] ", nullEnding())
	}
	if host == "shop.fpt.vn" && !strings.Contains(longURL, "shop.fpt.vn/tin-tuc/cong-tac-vien") {
		return nil, errData("Đường link dài không hợp lệ ctv. [shop.fpt.vn]", nullEnding())
	}
	if !contains(s.cfg.DomainAllow, host) {
		return nil, errData("Domain không hợp lệ", nullEnding())
	}
	if len(longURL) > maxLongURL {
		return nil, errData("Liên kết dài của bạn dài hơn chiều dài tối đa cho phép", nullEnding())
	}

	// 3. link (kể cả custom) đã có của user → trả lại (hệ cũ trả is_new = true ở nhánh này)
	if l, err := s.st.OwnerLinkByLongURL(ctx, u.Username, longURL, nil); err == nil {
		return &ShortenResult{ShortURL: s.FormatLink(l.Code, s.cfg.DefaultPrefix), Ending: l.Code, IsNew: true}, nil
	}

	domainID := s.domainID(ctx, domainParam)
	if custom != "" {
		if !shortcode.ValidEnding(custom) {
			return nil, errData("Đường dẫn rút gọn chỉ có thể chứa các ký tự chữ và số, dấu gạch nối và dấu gạch dưới", nullEnding())
		}
		ex, err := s.st.LinkByCode(ctx, custom)
		switch {
		case err == nil && ex.Status == domain.StatusDeleted:
			return nil, errData("Link rút gọn này đã xóa!", nullEnding())
		case err == nil:
			// Hệ cũ: cập nhật long_url của mã (chỉ khi cùng chủ) rồi trả error = 1 kèm link.
			if ex.OwnerUsername == u.Username {
				if l, uerr := s.st.UpdateLongURL(ctx, custom, longURL, s.ctvSet(longURL)); uerr == nil {
					s.changed(ctx, events.LinkUpdated, l)
				}
			}
			return nil, errData("Đã cập nhật link thành công", o(
				"short_url", s.FormatLink(ex.Code, s.cfg.DefaultPrefix), "ending", ex.Code,
			))
		case !errors.Is(err, store.ErrNotFound):
			return nil, err
		}
		l := s.newLink(u, longURL, custom, true, domain.APIv1, s.cfg.DefaultPrefix, "", 0, ip, domainID)
		if err := s.st.InsertLink(ctx, l); err != nil {
			if store.IsDuplicate(err) {
				return nil, errData("Đường dẫn rút gọn này đã được sử dụng", nullEnding())
			}
			return nil, err
		}
		s.created(ctx, u, l)
		return &ShortenResult{ShortURL: s.FormatLink(l.Code, l.Prefix), Ending: l.Code, IsNew: true}, nil
	}

	l := s.newLink(u, longURL, "", false, domain.APIv1, s.cfg.DefaultPrefix, "", 0, ip, domainID)
	if err := s.insertRandom(ctx, l, s.cfg.RandomKeyLength); err != nil {
		return nil, err
	}
	s.created(ctx, u, l)
	return &ShortenResult{ShortURL: s.FormatLink(l.Code, l.Prefix), Ending: l.Code, IsNew: true}, nil
}

func (s *Shortener) ctvSet(longURL string) bson.D {
	p := urlparams.Parse(longURL, nil, s.salt)
	return bson.D{{Key: "ctv_raw", Value: p.CTVRaw}, {Key: "ctv_id", Value: p.CTVID}, {Key: "dest_host", Value: p.DestHost}}
}

func (s *Shortener) domainID(ctx context.Context, d string) int64 {
	host := hostOf(d)
	if host == "" {
		return 0
	}
	ds, err := s.st.ActiveDomains(ctx)
	if err != nil {
		return 0
	}
	for _, x := range ds {
		if strings.EqualFold(x.DomainName, host) {
			return x.ID
		}
	}
	return 0
}

// ---------- v2 / v3 single ----------

// ShortenUser: /api/v2/shorten và /api/v3/shorten (LinkFactory::createLinkApiUser).
// prefix: "" = v2 (DEFAULT_PREFIX); v3 truyền PrefixV3(u).
func (s *Shortener) ShortenUser(ctx context.Context, u *domain.User, longURL, custom, campaignCode string, campaignID int64, ip, apiVersion, prefix string) (*ShortenResult, error) {
	longURL = strings.TrimSpace(longURL)
	if prefix == "" {
		prefix = s.cfg.DefaultPrefix
	}
	if len(longURL) > maxLongURL {
		return nil, errData("Liên kết dài của bạn dài hơn chiều dài tối đa cho phép", nullEnding())
	}
	// link chưa xoá của user có cùng long_url → trả lại (is_new = false)
	if l, err := s.st.OwnerLinkByLongURL(ctx, u.Username, longURL, nil); err == nil {
		return &ShortenResult{ShortURL: s.FormatLink(l.Code, prefix), Ending: l.Code}, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	// Trong createLinkApiUser cũ, mọi lỗi trong khối try (kể cả ApiException) bị bắt lại và ném
	// "Link dài này đã được sử dụng" không kèm data → giữ nguyên thông báo đó.
	errUsed := errData("Link dài này đã được sử dụng", nil)
	if custom != "" {
		if !shortcode.ValidEnding(custom) {
			return nil, errUsed
		}
		ex, err := s.st.LinkByCode(ctx, custom)
		switch {
		case err == nil && ex.Status == domain.StatusDeleted:
			return nil, errUsed
		case err == nil:
			// Mã đã có (của bất kỳ ai, kể cả bị khoá) → trả lại link đó, is_new = false (D4 chưa chốt).
			return &ShortenResult{ShortURL: s.FormatLink(ex.Code, prefix), Ending: ex.Code}, nil
		case !errors.Is(err, store.ErrNotFound):
			return nil, err
		}
		l := s.newLink(u, longURL, custom, true, apiVersion, prefix, campaignCode, campaignID, ip, 0)
		if err := s.st.InsertLink(ctx, l); err != nil {
			if store.IsDuplicate(err) {
				return nil, errUsed
			}
			return nil, err
		}
		s.created(ctx, u, l)
		return &ShortenResult{ShortURL: s.FormatLink(l.Code, prefix), Ending: l.Code, IsNew: true}, nil
	}

	l := s.newLink(u, longURL, "", false, apiVersion, prefix, campaignCode, campaignID, ip, 0)
	if err := s.insertRandom(ctx, l, s.keyLen(u)); err != nil {
		return nil, err
	}
	s.created(ctx, u, l)
	return &ShortenResult{ShortURL: s.FormatLink(l.Code, prefix), Ending: l.Code, IsNew: true}, nil
}

// ---------- multi (v2 shorten-multi, v3 shorten-multi) ----------

type MultiItem struct {
	Path     string
	Custom   string
	KeyItem  any
	Code     string
	Error    string // lỗi đầu vào (đã xác định trước)
	Campaign int64
}

type MultiResult struct {
	ShortURL *string `json:"short_url"`
	Ending   *string `json:"ending"`
	KeyItem  any     `json:"key_item"`
	Code     string  `json:"code"`
	IsExist  int     `json:"is_exist"`
	Error    *string `json:"error"`
}

// ShortenMulti: port LinkFactory::insertMultiLinksV3 (dùng cho cả v2 — sửa D10 và lỗi ghi đè của v2).
//   - Tái dùng link active CỦA CHÍNH user theo (long_url, campaign_code).
//   - custom_ending đã có chủ khác / đã xoá / bị khoá → lỗi riêng item, không ghi đè.
//   - Trùng (path, code) trong lô → dùng chung một mã.
func (s *Shortener) ShortenMulti(ctx context.Context, u *domain.User, items []MultiItem, apiVersion, prefix string) ([]MultiResult, error) {
	type st struct {
		ending  string
		custom  bool
		isExist int
		err     string
	}
	states := make([]st, len(items))
	var paths, customs []string
	for i, it := range items {
		states[i].err = it.Error
		if it.Error != "" {
			continue
		}
		paths = append(paths, it.Path)
		if it.Custom != "" {
			customs = append(customs, it.Custom)
		}
	}
	reuse, err := s.st.OwnerLinksByLongURLs(ctx, u.Username, paths)
	if err != nil {
		return nil, err
	}
	owners, err := s.st.LinksByCodes(ctx, customs)
	if err != nil {
		return nil, err
	}

	reserved := map[string]bool{}
	var need []int
	for i, it := range items {
		if states[i].err != "" {
			continue
		}
		if it.Custom != "" {
			if reserved[it.Custom] {
				states[i].err = "Link rút gọn bị trùng trong cùng một lô: " + it.Custom
				continue
			}
			reserved[it.Custom] = true
			states[i].ending, states[i].custom = it.Custom, true
			ex, ok := owners[it.Custom]
			switch {
			case !ok:
			case ex.Status == domain.StatusDeleted:
				states[i].ending, states[i].err = "", "Link rút gọn này đã xóa!"
			case ex.Status == domain.StatusDisabled:
				states[i].ending, states[i].err = "", "Link rút gọn này đang bị khoá!"
			case ex.OwnerUsername != u.Username:
				states[i].ending, states[i].err = "", "Đường dẫn rút gọn này đã được sử dụng"
			default:
				states[i].isExist = 1 // mã của chính mình → trả nguyên trạng
			}
			continue
		}
		if code, ok := reuse[it.Path+"\x00"+it.Code]; ok {
			states[i].ending, states[i].isExist = code, 1
			reserved[code] = true
			continue
		}
		need = append(need, i)
	}

	// Mã ngẫu nhiên cho phần còn lại; trùng (path, code) trong lô dùng chung.
	assigned := map[string]string{}
	var toInsert []*domain.Link
	var insertIdx []int
	for _, i := range need {
		k := items[i].Path + "\x00" + items[i].Code
		if code, ok := assigned[k]; ok {
			states[i].ending, states[i].isExist = code, 1
			continue
		}
		code := s.freshCode(reserved, s.keyLen(u))
		assigned[k] = code
		states[i].ending = code
	}
	for i, it := range items {
		if states[i].err != "" || states[i].isExist == 1 || states[i].ending == "" {
			continue
		}
		toInsert = append(toInsert, s.newLink(u, it.Path, states[i].ending, states[i].custom, apiVersion, prefix, it.Code, it.Campaign, "", 0))
		insertIdx = append(insertIdx, i)
	}

	// Ghi; mã ngẫu nhiên đụng độ (hiếm) → sinh lại và ghi riêng.
	dups, err := s.st.InsertLinks(ctx, toInsert)
	if err != nil {
		return nil, err
	}
	var ok []*domain.Link
	for j, l := range toInsert {
		i := insertIdx[j]
		if !dups[j] {
			ok = append(ok, l)
			continue
		}
		if states[i].custom {
			states[i].ending, states[i].err = "", "Đường dẫn rút gọn này đã được sử dụng"
			continue
		}
		if err := s.insertRandom(ctx, l, s.keyLen(u)); err != nil {
			states[i].ending, states[i].err = "", err.Error()
			continue
		}
		states[i].ending = l.Code
		ok = append(ok, l)
	}
	s.created(ctx, u, ok...)

	out := make([]MultiResult, len(items))
	for i, it := range items {
		r := MultiResult{KeyItem: it.KeyItem, Code: it.Code, IsExist: states[i].isExist}
		if r.KeyItem == nil {
			r.KeyItem = ""
		}
		if states[i].err == "" {
			su, e := s.FormatLink(states[i].ending, prefix), states[i].ending
			r.ShortURL, r.Ending = &su, &e
		} else {
			msg := states[i].err
			r.Error = &msg
		}
		out[i] = r
	}
	return out, nil
}

func (s *Shortener) freshCode(reserved map[string]bool, n int) string {
	for {
		c := shortcode.Random(n)
		if !reserved[c] {
			reserved[c] = true
			return c
		}
	}
}
