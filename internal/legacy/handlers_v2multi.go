package legacy

import (
	"errors"
	"net/http"
	"strings"

	"am-shortlink-service/internal/domain"
	"am-shortlink-service/internal/platform/httpx"
	"am-shortlink-service/internal/store"
)

// campaignExists: điều kiện `!$campaign && ...` của hệ cũ — chỉ báo lỗi khi KHÔNG có campaign với mã đó
// (không kiểm chủ — D10 chưa chốt). Trả campaign hoặc nil.
func (h *Handlers) campaignExists(w http.ResponseWriter, r *http.Request, code string) (*domain.Campaign, bool) {
	c, err := h.st.CampaignByCode(r.Context(), code)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		h.internalError(w, r, err)
		return nil, false
	}
	if c == nil {
		fail(w, "Campaign code không hợp lệ.", nil)
		return nil, false
	}
	return c, true
}

// v2ShortenMulti: POST /api/v2/shorten-multi — giữ nguyên input / output của ApiLinkController::shortenLinkV2Multi.
func (h *Handlers) v2ShortenMulti(w http.ResponseWriter, r *http.Request) {
	in := inputFrom(r)
	if msg := firstError(in, []rule{
		required("urls", "Đường link dài không được trống!"), isArray("urls", "Đường link dài không đúng định dạng!"),
		minLen("code", 5, "Code chiến dịch phải lớn hơn 5 ký tự!"), maxLen("code", 40, "Code chiến dịch  không được lớn hơn 40 ký tự!"),
	}); msg != "" {
		fail(w, msg, nullEnding())
		return
	}
	code := in.Str("code")
	c, okc := h.campaignExists(w, r, code)
	if !okc {
		return
	}
	urls, _ := in.List("urls")
	if len(urls) > maxMulti {
		fail(w, "Số lượng urls quá 1000.", nil)
		return
	}
	items := make([]V2MultiItem, len(urls))
	for i, v := range urls {
		if m, isObj := item(v); isObj {
			items[i] = V2MultiItem{Path: itemStr(m, "path"), Custom: itemStr(m, "custom_ending"), KeyItem: m["key_item"]}
		}
	}
	res, err := h.sh.ShortenMultiV2(r.Context(), userFrom(r), items, code, c.ID)
	if err != nil {
		h.respondErr(w, r, err)
		return
	}
	ok(w, "", res)
}

// v2UpdateMulti: POST /api/v2/update-shorten-multi (≤ 100 url).
func (h *Handlers) v2UpdateMulti(w http.ResponseWriter, r *http.Request) {
	in := inputFrom(r)
	if msg := firstError(in, []rule{
		required("urls", "Đường link dài không được trống!"), isArray("urls", "Đường link dài không đúng định dạng!"),
		minLen("code", 5, "Code chiến dịch phải lớn hơn 5 ký tự!"), maxLen("code", 40, "Code chiến dịch  không được lớn hơn 40 ký tự!"),
	}); msg != "" {
		fail(w, msg, nullEnding())
		return
	}
	code := in.Str("code")
	c, okc := h.campaignExists(w, r, code)
	if !okc {
		return
	}
	urls, _ := in.List("urls")
	if len(urls) > 100 {
		fail(w, "Số lượng urls quá 100.", nil)
		return
	}
	items := make([]UpdateItem, 0, len(urls))
	for _, v := range urls {
		m, isObj := item(v)
		if !isObj {
			items = append(items, UpdateItem{})
			continue
		}
		items = append(items, UpdateItem{Path: itemStr(m, "path"), Custom: lastSegmentRaw(itemStr(m, "custom_ending")), KeyItem: m["key_item"]})
	}
	res, err := h.sh.UpdateMulti(r.Context(), userFrom(r), items, code, c.ID, httpx.ClientIP(r))
	if err != nil {
		h.respondErr(w, r, err)
		return
	}
	ok(w, "", res)
}

// lastSegmentRaw: explode("/") + end() của PHP (không trim).
func lastSegmentRaw(s string) string {
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		return s[i+1:]
	}
	return s
}
