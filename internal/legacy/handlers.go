package legacy

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skip2/go-qrcode"
	"go.mongodb.org/mongo-driver/v2/bson"

	"am-shortlink-service/internal/analytics"
	"am-shortlink-service/internal/config"
	"am-shortlink-service/internal/domain"
	"am-shortlink-service/internal/events"
	"am-shortlink-service/internal/linkcache"
	"am-shortlink-service/internal/platform/httpx"
	"am-shortlink-service/internal/platform/redisx"
	"am-shortlink-service/internal/shortcode"
	"am-shortlink-service/internal/store"
	"am-shortlink-service/internal/urlparams"
)

type Handlers struct {
	st        *store.Store
	sh        *Shortener
	auth      *keyAuth
	quota     *quota
	links     *linkcache.Cache
	rdb       *redisx.Client
	cfg       config.API
	log       *slog.Logger
	rl        *rateLimiter
	allowNets []*net.IPNet
	fwdHosts  map[string]bool

	domMu     sync.Mutex
	domains   map[string]bool
	domLoaded time.Time
}

func NewHandlers(st *store.Store, links *linkcache.Cache, rdb *redisx.Client, pub events.Publisher, cfg *config.Config, log *slog.Logger) *Handlers {
	q := &quota{st: st, rdb: rdb}
	h := &Handlers{
		st: st, auth: newKeyAuth(st), quota: q, links: links, rdb: rdb, cfg: cfg.API, log: log,
		rl: newRateLimiter(cfg.API.RateLimitPerSecond), allowNets: parseNets(cfg.API.AllowIP), fwdHosts: map[string]bool{},
	}
	for _, x := range cfg.API.ForwardedHosts {
		if x = strings.ToLower(strings.TrimSpace(x)); x != "" {
			h.fwdHosts[x] = true
		}
	}
	h.sh = &Shortener{st: st, links: links, pub: pub, quota: q, cfg: cfg.API, salt: cfg.PIIHashSalt}
	return h
}

// Routes: đúng nhóm middleware của routes.php cũ (thứ tự: api → checkIP → checkForwardedHost).
func (h *Handlers) Routes(r chi.Router) {
	r.Use(recoverer(h.log), withInput)

	// /api/v1 — link_avail_check (không cần key)
	r.With(h.checkIP, h.forwardedHost).Post("/api/v1/link_avail_check", h.linkAvailCheck)

	r.Group(func(r chi.Router) {
		r.Use(h.apiAuth, h.checkIP, h.forwardedHost)
		r.Post("/api/v1/shorten", h.v1Shorten)
		r.Post("/api/v1/qrcode", h.v1QRCode)
		r.Post("/api/v1/delete", h.v1Delete)
		r.Post("/api/v1/restore", h.v1Restore)

		r.With(h.rateLimit).Post("/api/v2/shorten", h.v2Shorten)
		r.With(h.rateLimit).Post("/api/v2/shorten-multi", h.v2ShortenMulti)
		r.Post("/api/v2/update-shorten-multi", h.v2UpdateMulti)
		r.Post("/api/v2/report", h.v2Report)
		r.Post("/api/v2/search", h.v2Search)
		r.Post("/api/v2/update-links-ctv-identifier", h.v2UpdateCTV)
		r.Post("/api/v2/cache-clear", h.v2CacheClear)
		r.Post("/api/v2/cache-check-key", h.v2CacheCheck)
		r.Post("/api/v2/cache-clear-key", h.v2CacheClearKey)

		r.With(h.rateLimit).Post("/api/v3/shorten", h.v3Shorten)
		r.With(h.rateLimit).Post("/api/v3/shorten-multi", h.v3ShortenMulti)
	})

	// /api/v2/campaign, /api/v2/template: chỉ api + checkForwardedHost (không checkIP — như hệ cũ)
	r.Group(func(r chi.Router) {
		r.Use(h.apiAuth, h.forwardedHost)
		r.Post("/api/v2/campaign/list", h.campaignList)
		r.Post("/api/v2/campaign/create", h.campaignCreate)
		r.Get("/api/v2/template/list", h.templateList)
		r.Post("/api/v2/template/list", h.templateList)
		r.Get("/api/v2/template/detail", h.templateDetail)
		r.Post("/api/v2/template/detail", h.templateDetail)
	})
}

// respondErr: *apiError → error = 1 kèm data; lỗi khác → lỗi chung.
func (h *Handlers) respondErr(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		fail(w, ae.Msg, ae.Data)
		return
	}
	h.internalError(w, r, err)
}

// ---------- v1 ----------

func (h *Handlers) linkAvailCheck(w http.ResponseWriter, r *http.Request) {
	e := inputFrom(r).Str("link_ending")
	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	switch {
	case !shortcode.ValidEnding(e):
		_, _ = w.Write([]byte("invalid"))
	default:
		_, err := h.st.LinkByCode(r.Context(), e)
		if err == nil {
			_, _ = w.Write([]byte("unavailable"))
		} else if errors.Is(err, store.ErrNotFound) {
			_, _ = w.Write([]byte("available"))
		} else {
			h.internalError(w, r, err)
		}
	}
}

func (h *Handlers) v1Shorten(w http.ResponseWriter, r *http.Request) {
	in := inputFrom(r)
	if msg := firstError(in, []rule{
		required("url", "Đường link dài không được trống!"), urlRule("url", "Đường link dài không đúng định dạng!"),
		required("domain", "Domain không được trống!"), urlRule("domain", "Domain không đúng định dạng!"),
		minLen("custom_ending", 5, "Link rút gọn phải lớn hơn 5 ký tự!"), maxLen("custom_ending", 50, "Link rút gọn không được lớn hơn 50 ký tự!"),
	}); msg != "" {
		fail(w, msg, nullEnding())
		return
	}
	res, err := h.sh.ShortenV1(r.Context(), userFrom(r), in.Str("url"), in.Str("domain"), strings.TrimSpace(in.Str("custom_ending")), httpx.ClientIP(r))
	if err != nil {
		h.respondErr(w, r, err)
		return
	}
	if res.ExistingV1 {
		ok(w, msgOK, o("short_url", res.ShortURL, "long_url", res.LongURL, "ending", res.Ending, "is_new", false))
		return
	}
	ok(w, "", o("short_url", res.ShortURL, "ending", res.Ending, "is_new", res.IsNew))
}

func (h *Handlers) v1QRCode(w http.ResponseWriter, r *http.Request) {
	in := inputFrom(r)
	if msg := firstError(in, []rule{
		required("short_url", "Link rút gọn không được trống!"), urlRule("short_url", "Link rút gọn không đúng định dạng!"),
		numeric("dimension", "Kích thước ảnh QR phải là số"),
	}); msg != "" {
		fail(w, msg, nil)
		return
	}
	size := int(in.Int("dimension", 512))
	size = int(math.Min(float64(size), 4096)) // chặn ảnh quá lớn gây hết RAM (kích thước thường dùng không đổi)
	png, err := qrcode.Encode(in.Str("short_url"), qrcode.Low, size)
	if err != nil {
		fail(w, err.Error(), nil)
		return
	}
	success(w, o("qr_code", "data:image/png;base64,"+base64.StdEncoding.EncodeToString(png)))
}

func (h *Handlers) v1DeleteRestore(w http.ResponseWriter, r *http.Request, del bool) {
	in := inputFrom(r)
	if msg := firstError(in, []rule{
		required("short_url", "Link rút gọn không được trống!"), urlRule("short_url", "Link rút gọn không đúng định dạng!"),
	}); msg != "" {
		fail(w, msg, nil)
		return
	}
	if err := h.sh.SetDeleted(r.Context(), in.Str("short_url"), del); err != nil {
		h.respondErr(w, r, err)
		return
	}
	success(w, nil)
}

func (h *Handlers) v1Delete(w http.ResponseWriter, r *http.Request)  { h.v1DeleteRestore(w, r, true) }
func (h *Handlers) v1Restore(w http.ResponseWriter, r *http.Request) { h.v1DeleteRestore(w, r, false) }

// ---------- v2 / v3 shorten ----------

var shortenRules = []rule{
	required("url", "Đường link dài không được trống!"), urlRule("url", "Đường link dài không đúng định dạng!"),
	minLen("custom_ending", 5, "Link rút gọn phải lớn hơn 5 ký tự!"), maxLen("custom_ending", 50, "Link rút gọn không được lớn hơn 50 ký tự!"),
	minLen("code", 5, "Code chiến dịch phải lớn hơn 5 ký tự!"), maxLen("code", 40, "Code chiến dịch  không được lớn hơn 40 ký tự!"),
}

// ownedCampaign: mã campaign (nếu có) phải thuộc user. Trả (id, ok).
func (h *Handlers) ownedCampaign(ctx context.Context, u *domain.User, code string) (int64, bool, error) {
	if code == "" {
		return 0, true, nil
	}
	c, err := h.st.CampaignByCode(ctx, code)
	if errors.Is(err, store.ErrNotFound) {
		return 0, true, nil // single: mã không tồn tại → bỏ qua (campaign_id = 0), như hệ cũ
	}
	if err != nil {
		return 0, false, err
	}
	if !c.OwnedBy(u) {
		return 0, false, nil
	}
	return c.ID, true, nil
}

func (h *Handlers) shortenSingle(w http.ResponseWriter, r *http.Request, v3 bool) {
	in := inputFrom(r)
	if msg := firstError(in, shortenRules); msg != "" {
		fail(w, msg, nullEnding())
		return
	}
	u := userFrom(r)
	code := strings.TrimSpace(in.Str("code"))
	cid, owned, err := h.ownedCampaign(r.Context(), u, code)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	if !owned {
		if v3 {
			fail(w, "Campaign code không hợp lệ.", nullEnding())
		} else {
			fail(w, "Campaign code không hợp lệ.", nil)
		}
		return
	}
	ver, prefix := domain.APIv2, ""
	if v3 {
		ver, prefix = domain.APIv3, h.sh.PrefixV3(u)
	}
	res, err := h.sh.ShortenUser(r.Context(), u, in.Str("url"), strings.TrimSpace(in.Str("custom_ending")), code, cid, httpx.ClientIP(r), ver, prefix)
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) {
			fail(w, ae.Msg, ae.Data)
			return
		}
		h.internalError(w, r, err)
		return
	}
	if v3 {
		ok(w, "", o("short_url", res.ShortURL, "ending", res.Ending))
		return
	}
	ok(w, "", o("short_url", res.ShortURL, "ending", res.Ending, "is_new", res.IsNew))
}

func (h *Handlers) v2Shorten(w http.ResponseWriter, r *http.Request) { h.shortenSingle(w, r, false) }
func (h *Handlers) v3Shorten(w http.ResponseWriter, r *http.Request) { h.shortenSingle(w, r, true) }

// ---------- multi ----------

const maxMulti = 1000

// v3ShortenMulti: port ShortenController::shortenMultiAction (code theo từng item, lỗi theo item).
func (h *Handlers) v3ShortenMulti(w http.ResponseWriter, r *http.Request) {
	in := inputFrom(r)
	if msg := firstError(in, []rule{
		required("urls", "Đường link dài không được trống!"), isArray("urls", "Đường link dài không đúng định dạng!"),
		minLen("code", 5, "Code chiến dịch phải lớn hơn 5 ký tự!"), maxLen("code", 40, "Code chiến dịch  không được lớn hơn 40 ký tự!"),
	}); msg != "" {
		fail(w, msg, nullEnding())
		return
	}
	urls, _ := in.List("urls")
	if len(urls) > maxMulti {
		fail(w, fmt.Sprintf("Số lượng urls quá %d.", maxMulti), nullEnding())
		return
	}
	// Lỗi định dạng ở mức request: báo dòng đầu tiên sai, không tạo gì.
	defCode := strings.TrimSpace(in.Str("code"))
	items := make([]MultiItem, len(urls))
	codes := map[string]bool{}
	if defCode != "" {
		codes[defCode] = true
	}
	for i, v := range urls {
		line := itoa(i + 1)
		m, isObj := item(v)
		if !isObj {
			fail(w, "Dòng "+line+": mỗi phần tử của urls phải là một object.", nullEnding())
			return
		}
		_, hasPath := m["path"]
		_, hasURL := m["url"]
		if !hasPath && !hasURL {
			fail(w, "Dòng "+line+": thiếu trường url (hoặc path).", nullEnding())
			return
		}
		path := strings.TrimSpace(itemStr(m, "url"))
		if hasPath {
			path = strings.TrimSpace(itemStr(m, "path"))
		}
		switch {
		case path == "":
			fail(w, "Dòng "+line+": đường link dài không được trống!", nullEnding())
			return
		case len(path) > maxLongURL:
			fail(w, "Dòng "+line+": liên kết của bạn dài hơn chiều dài tối đa cho phép.", nullEnding())
			return
		case !isURL(encodeSpaces(path)):
			fail(w, "Dòng "+line+": đường link dài không đúng định dạng!", nullEnding())
			return
		}
		code := strings.TrimSpace(itemStr(m, "code"))
		if _, has := m["code"]; has {
			if code != "" && len(code) < 5 {
				fail(w, "Dòng "+line+": code chiến dịch phải lớn hơn 5 ký tự!", nullEnding())
				return
			}
			if len(code) > 40 {
				fail(w, "Dòng "+line+": code chiến dịch không được lớn hơn 40 ký tự!", nullEnding())
				return
			}
		}
		custom := strings.TrimSpace(itemStr(m, "custom_ending"))
		if custom != "" {
			switch {
			case !shortcode.ValidEnding(custom):
				fail(w, "Dòng "+line+": đường dẫn rút gọn chỉ có thể chứa các ký tự chữ và số, dấu gạch nối và dấu gạch dưới.", nullEnding())
				return
			case len(custom) < 5:
				fail(w, "Dòng "+line+": link rút gọn phải lớn hơn 5 ký tự!", nullEnding())
				return
			case len(custom) > 50:
				fail(w, "Dòng "+line+": link rút gọn không được lớn hơn 50 ký tự!", nullEnding())
				return
			}
		}
		if code == "" {
			code = defCode
		}
		if code != "" {
			codes[code] = true
		}
		items[i] = MultiItem{Path: path, Custom: custom, KeyItem: m["key_item"], Code: code}
	}

	// Campaign theo item: chỉ nhận campaign của chính user.
	u := userFrom(r)
	list := make([]string, 0, len(codes))
	for c := range codes {
		list = append(list, c)
	}
	cs, err := h.st.CampaignsByCodes(r.Context(), list)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	owned := map[string]int64{}
	for i := range cs {
		if cs[i].OwnedBy(u) {
			owned[cs[i].Code] = cs[i].ID
		}
	}
	for i := range items {
		if items[i].Code == "" {
			continue
		}
		id, okc := owned[items[i].Code]
		if !okc {
			items[i].Error = "Campaign code không hợp lệ."
			continue
		}
		items[i].Campaign = id
	}
	res, err := h.sh.ShortenMulti(r.Context(), u, items, domain.APIv3, h.sh.PrefixV3(u))
	if err != nil {
		h.respondErr(w, r, err)
		return
	}
	ok(w, "", res)
}

// ---------- v2 search / report ----------

// v2Search: tra long_url theo short_url (mọi user — giữ như hệ cũ).
func (h *Handlers) v2Search(w http.ResponseWriter, r *http.Request) {
	in := inputFrom(r)
	if msg := firstError(in, []rule{
		required("url", "Đường link không được trống!"), urlRule("url", "Đường link không đúng định dạng!"),
	}); msg != "" {
		fail(w, msg, nullEnding())
		return
	}
	l, err := h.st.LinkByCode(r.Context(), lastSegmentRaw(in.Str("url")))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		h.internalError(w, r, err)
		return
	}
	if l == nil {
		ok(w, "", []any{})
		return
	}
	ok(w, msgOK, o("short_url", in.Str("url"), "long_url", l.LongURL))
}

// v2Report: POST /api/v2/report — danh sách link theo campaign (handler ApiLinkController@reportShortenLink, D12).
func (h *Handlers) v2Report(w http.ResponseWriter, r *http.Request) {
	in := inputFrom(r)
	if msg := firstError(in, []rule{
		required("code", "x"), numeric("display", "x"), maxNum("display", 1000, "x"),
		required("type", "x"), oneOf("type", []string{"all", "click"}, "x"),
	}); msg != "" {
		fail(w, "Invalid or missing parameters.", nil) // hệ cũ luôn trả thông báo chung này
		return
	}
	u := userFrom(r)
	page, per := pageParams(in, "page", "display", 100)
	links, total, err := h.st.OwnerCampaignLinks(r.Context(), u.Username, in.Str("code"), in.Str("type") == "click", page, per)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	rows := make([]obj, len(links))
	for i, l := range links {
		rows[i] = o(
			"short_url", l.Code, "long_url", l.LongURL, "domain_id", l.DomainID, "ip", l.IP, "creator", l.OwnerUsername,
			"clicks", l.Clicks, "secret_key", l.SecretKey, "is_disabled", b2i(l.Status == domain.StatusDisabled),
			"is_deleted", b2i(l.Status == domain.StatusDeleted), "is_custom", b2i(l.IsCustom), "is_api", b2i(l.IsAPI),
			"created_at", fmtTime(l.CreatedAt), "long_url_hash", l.LongURLHash,
		)
	}
	ok(w, "", paginator(rows, total, page, per))
}

// v2UpdateCTV: tính lại ctv_raw / ctv_id từ long_url. Nhận `creator` từ request như hệ cũ (D6 chưa chốt).
func (h *Handlers) v2UpdateCTV(w http.ResponseWriter, r *http.Request) {
	in := inputFrom(r)
	if msg := firstError(in, []rule{
		numeric("page", "Page phải là số!"), minNum("page", 1, "Page tối thiểu là 1!"),
		numeric("limit", "Limit phải là số!"), minNum("limit", 1, "Limit tối thiểu là 1!"), maxNum("limit", 1000, "Limit tối đa là 1000!"),
	}); msg != "" {
		fail(w, msg, []any{})
		return
	}
	u := userFrom(r)
	creator := u.Username
	if c := in.Str("creator"); c != "" {
		creator = c
	}
	page, per := pageParams(in, "page", "limit", 100)
	links, total, err := h.st.OwnerLinksPage(r.Context(), creator, page, per)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	updated := 0
	for i := range links {
		p := urlparams.Parse(links[i].LongURL, nil, "")
		if p.CTVRaw == links[i].CTVRaw && p.CTVID == links[i].CTVID {
			continue
		}
		if err := h.st.SetLinkFields(r.Context(), links[i].ID, bson.D{{Key: "ctv_raw", Value: p.CTVRaw}, {Key: "ctv_id", Value: p.CTVID}}); err != nil {
			h.internalError(w, r, err)
			return
		}
		links[i].CTVRaw, links[i].CTVID = p.CTVRaw, p.CTVID
		h.sh.changed(r.Context(), events.LinkUpdated, &links[i])
		updated++
	}
	ok(w, "", o("updated", updated, "total", total, "current_page", page, "per_page", per))
}

// ---------- cache ----------

// v2CacheClear: mọi key gọi được như hệ cũ (D7 chưa chốt); chỉ xoá cache link (link:*), không flush toàn Redis.
func (h *Handlers) v2CacheClear(w http.ResponseWriter, r *http.Request) {
	if h.rdb != nil {
		var cursor uint64
		for {
			keys, next, err := h.rdb.Scan(r.Context(), cursor, h.rdb.Key("link", "*"), 1000).Result()
			if err != nil {
				fail(w, "Error clearing cache: "+err.Error(), nil)
				return
			}
			if len(keys) > 0 {
				h.rdb.Del(r.Context(), keys...)
			}
			if cursor = next; cursor == 0 {
				break
			}
		}
	}
	ok(w, "Cache cleared successfully", o("cleared_at", fmtTime(time.Now()), "message", "Tất cả cache đã được xóa"))
}

// cacheCode: short_url (mã hoặc URL đầy đủ) hoặc cache_key dạng "performRedirect:{code}".
func cacheCode(in Input) (key, code string) {
	if s := strings.TrimSpace(in.Str("short_url")); s != "" {
		c := lastSegment(s)
		return "performRedirect:" + c, c
	}
	k := strings.TrimSpace(in.Str("cache_key"))
	return k, strings.TrimPrefix(k, "performRedirect:")
}

func (h *Handlers) v2CacheCheck(w http.ResponseWriter, r *http.Request) {
	key, code := cacheCode(inputFrom(r))
	if key == "" {
		fail(w, "Thiếu tham số short_url hoặc cache_key", nil)
		return
	}
	var exists bool
	var value any
	if h.rdb != nil && code != "" {
		if n, err := h.rdb.Exists(r.Context(), h.rdb.Key("link", code)).Result(); err == nil && n > 0 {
			exists = true
			if l, err := h.st.LinkByCode(r.Context(), code); err == nil {
				value = o("id", l.ID, "short_url", l.Code, "long_url", l.LongURL,
					"is_disabled", b2i(l.Status == domain.StatusDisabled), "is_deleted", b2i(l.Status == domain.StatusDeleted))
			}
		}
	}
	ok(w, "", o("cache_key", key, "exists", exists, "value", value))
}

func (h *Handlers) v2CacheClearKey(w http.ResponseWriter, r *http.Request) {
	key, code := cacheCode(inputFrom(r))
	if key == "" {
		fail(w, "Thiếu tham số short_url hoặc cache_key", nil)
		return
	}
	existed := false
	if h.rdb != nil && code != "" {
		if n, err := h.rdb.Exists(r.Context(), h.rdb.Key("link", code)).Result(); err == nil {
			existed = n > 0
		}
	}
	if code != "" {
		h.links.Invalidate(r.Context(), code)
	}
	msg := "Cache key không tồn tại"
	if existed {
		msg = "Cache key cleared successfully"
	}
	ok(w, msg, o("cache_key", key, "existed", existed, "cleared_at", fmtTime(time.Now())))
}

// ---------- campaign / template ----------

func (h *Handlers) campaignList(w http.ResponseWriter, r *http.Request) {
	in := inputFrom(r)
	if errs := allErrors(in, []rule{
		numeric("display", "The display must be a number."), maxNum("display", 1000, "Số dòng dữ liệu phải nhỏ hơn hoặc bằng 1000"),
	}); errs != nil {
		fail(w, errs, nil)
		return
	}
	u := userFrom(r)
	page, per := pageParams(in, "page", "display", 100)
	cs, total, err := h.st.ListCampaigns(r.Context(), store.CampaignFilter{
		CreatedBy: u.ID, Name: in.Str("name"), Code: in.Str("code"), Page: page, PerPage: per,
	})
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	rows := make([]obj, len(cs))
	for i, c := range cs {
		rows[i] = o("name", c.Name, "code", c.Code, "created_at", fmtTime(c.CreatedAt), "created_by", c.CreatedByID())
	}
	success(w, paginator(rows, total, page, per))
}

func (h *Handlers) campaignCreate(w http.ResponseWriter, r *http.Request) {
	in := inputFrom(r)
	nameTaken := false
	if in.Has("name") {
		exists, err := h.st.CampaignNameExists(r.Context(), in.Str("name"))
		if err != nil {
			h.internalError(w, r, err)
			return
		}
		nameTaken = exists
	}
	// Thứ tự luật = 'name' => 'required|string|max:255|unique:campaigns', 'code' => 'required|string|max:10'
	errs := allErrors(in, []rule{
		required("name", "Vui lòng nhập tên chiến dịch."), isString("name", "Tên chiến dịch phải là dạng chuỗi ký tự."),
		maxLen("name", 255, "Tên chiến dịch không được vượt quá 255 ký tự"),
		{"name", func(Input) string {
			if nameTaken {
				return "Tên chiến dịch đã tồn tại trong hệ thống"
			}
			return ""
		}},
		required("code", "Vui lòng nhập mã chiến dịch."), isString("code", "Mã chiến dịch phải là dạng chuỗi ký tự."),
		maxLen("code", 10, "Mã chiến dịch không được vượt quá 10 ký tự."),
	})
	if errs != nil {
		fail(w, errs, nil)
		return
	}
	u := userFrom(r)
	// mã = code + dmY + uniqid() (giống processCreateCampaignParams)
	now := time.Now().In(analytics.VN)
	code := in.Str("code") + now.Format("02012006") + uniqid(time.Now())
	id, at, err := h.st.CreateCampaign(r.Context(), in.Str("name"), code, u.ID)
	if err != nil {
		if store.IsDuplicate(err) {
			fail(w, "Tên chiến dịch đã tồn tại trong hệ thống", nil)
			return
		}
		fail(w, err.Error(), nil)
		return
	}
	success(w, o("name", in.Str("name"), "code", code, "created_by", u.ID,
		"updated_at", fmtTime(at), "created_at", fmtTime(at), "id", id))
}

func templateRow(t *domain.Template) obj {
	return o("template_id", t.ID, "template_name", t.TemplateName, "template_url", t.TemplateURL, "template_images", t.TemplateImages)
}

func (h *Handlers) templateList(w http.ResponseWriter, r *http.Request) {
	ts, err := h.st.ActiveTemplates(r.Context())
	if err != nil {
		h.log.Error("template list", "err", err)
		fail(w, "Đã có lỗi xảy ra. Vui lòng thử lại", []any{})
		return
	}
	rows := make([]obj, len(ts))
	for i := range ts {
		rows[i] = templateRow(&ts[i])
	}
	success(w, rows)
}

func (h *Handlers) templateDetail(w http.ResponseWriter, r *http.Request) {
	t, err := h.st.TemplateByURL(r.Context(), inputFrom(r).Str("url"))
	if errors.Is(err, store.ErrNotFound) {
		fail(w, "Đường link landing page không tồn tại", []any{})
		return
	}
	if err != nil {
		h.log.Error("template detail", "err", err)
		fail(w, "Đã có lỗi xảy ra. Vui lòng thử lại", []any{})
		return
	}
	success(w, templateRow(t))
}

// ---------- tiện ích ----------

func pageParams(in Input, pageKey, perKey string, defPer int64) (int64, int64) {
	page := in.Int(pageKey, 1)
	if page < 1 {
		page = 1
	}
	per := in.Int(perKey, defPer)
	if per < 1 {
		per = defPer
	}
	if per > 1000 {
		per = 1000
	}
	return page, per
}

// paginator: LengthAwarePaginator::toArray() của Laravel 5.1 (đã bỏ next/prev_page_url như hệ cũ).
func paginator(rows any, total, page, per int64) obj {
	last := int64(math.Max(1, math.Ceil(float64(total)/float64(per))))
	var from, to any
	if total > 0 && (page-1)*per < total {
		f := (page-1)*per + 1
		t := f + int64(lenOf(rows)) - 1
		from, to = f, t
	}
	return o("total", total, "per_page", per, "current_page", page, "last_page", last, "from", from, "to", to, "data", rows)
}

func lenOf(rows any) int {
	switch t := rows.(type) {
	case []obj:
		return len(t)
	case []any:
		return len(t)
	}
	return 0
}

func fmtTime(t time.Time) string { return t.In(analytics.VN).Format("2006-01-02 15:04:05") }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

// uniqid: giống PHP uniqid() — 8 hex giây + 5 hex micro giây.
func uniqid(t time.Time) string {
	return fmt.Sprintf("%8x%05x", t.Unix(), t.Nanosecond()/1000)
}
