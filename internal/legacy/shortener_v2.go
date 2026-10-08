package legacy

import (
	"context"
	"errors"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"am-shortlink-service/internal/domain"
	"am-shortlink-service/internal/events"
	"am-shortlink-service/internal/store"
	"am-shortlink-service/internal/urlparams"
)

// ---------- v2 shorten-multi ----------

type V2MultiItem struct {
	Path    string
	Custom  string
	KeyItem any
}

// ShortenMultiV2: port nguyên LinkFactory::insertMultiLinks (v2) — giữ nguyên input / output:
//   - long_url đã có ở BẤT KỲ link nào (mọi user, mọi trạng thái) → dùng lại mã đó;
//   - ngược lại dùng custom_ending, rỗng → mã ngẫu nhiên;
//   - mã đã tồn tại → chỉ cập nhật long_url (INSERT ... ON DUPLICATE KEY UPDATE long_url cũ);
//   - kết quả {short_url, ending, key_item} theo đúng thứ tự input.
func (s *Shortener) ShortenMultiV2(ctx context.Context, u *domain.User, items []V2MultiItem, campaignCode string, campaignID int64) ([]obj, error) {
	paths := make([]string, len(items))
	for i, it := range items {
		paths[i] = strings.TrimSpace(it.Path)
	}
	existing, err := s.st.CodesByLongURLs(ctx, paths)
	if err != nil {
		return nil, err
	}
	codes := make([]string, len(items))
	reused := make([]bool, len(items))
	reserved := map[string]bool{}
	for i, it := range items {
		switch {
		case existing[paths[i]] != "":
			codes[i], reused[i] = existing[paths[i]], true
		case it.Custom != "":
			codes[i] = it.Custom
		}
		if codes[i] != "" {
			reserved[codes[i]] = true
		}
	}
	for i := range codes {
		if codes[i] == "" {
			codes[i] = s.freshCode(reserved, s.keyLen(u))
		}
	}
	have, err := s.st.LinksByCodes(ctx, codes)
	if err != nil {
		return nil, err
	}

	var toInsert []*domain.Link
	inBatch := map[string]*domain.Link{}
	for i, it := range items {
		code := codes[i]
		if l, ok := inBatch[code]; ok { // trùng mã trong lô → dòng sau ghi đè long_url dòng trước
			l.LongURL = paths[i]
			continue
		}
		if ex, ok := have[code]; ok {
			if ex.LongURL != paths[i] {
				l, uerr := s.st.UpdateLongURL(ctx, code, paths[i], s.ctvSet(paths[i]))
				if uerr != nil {
					return nil, uerr
				}
				s.changed(ctx, events.LinkUpdated, l)
			}
			continue
		}
		l := s.newLink(u, paths[i], code, it.Custom != "" || reused[i], domain.APIv2, s.cfg.DefaultPrefix, campaignCode, campaignID, "", 0)
		inBatch[code] = l
		toInsert = append(toInsert, l)
	}
	for _, l := range toInsert { // long_url có thể đã bị dòng sau trong lô đổi
		p := urlparams.Parse(l.LongURL, nil, s.salt)
		l.CTVRaw, l.CTVID, l.DestHost = p.CTVRaw, p.CTVID, p.DestHost
	}
	dups, err := s.st.InsertLinks(ctx, toInsert)
	if err != nil {
		return nil, err
	}
	var created []*domain.Link
	for j, l := range toInsert {
		if !dups[j] {
			created = append(created, l)
			continue
		}
		// Mã vừa bị chiếm (tranh chấp hiếm) → cập nhật long_url như ON DUPLICATE KEY UPDATE.
		if ul, uerr := s.st.UpdateLongURL(ctx, l.Code, l.LongURL, s.ctvSet(l.LongURL)); uerr == nil {
			s.changed(ctx, events.LinkUpdated, ul)
		}
	}
	s.created(ctx, u, created...)

	out := make([]obj, len(items))
	for i, it := range items {
		k := it.KeyItem
		if k == nil {
			k = ""
		}
		out[i] = o("short_url", s.FormatLink(codes[i], s.cfg.DefaultPrefix), "ending", codes[i], "key_item", k)
	}
	return out, nil
}

// ---------- update-shorten-multi (v2) ----------

type UpdateItem struct {
	Path    string
	Custom  string // mã (phần cuối của custom_ending)
	KeyItem any
}

// UpdateMulti: port LinkFactory::updateLinkMutilApiUser — updateOrCreate theo mã:
// mã có rồi (của bất kỳ ai) → ghi long_url, campaign, creator = người gọi; chưa có → tạo mới.
// Kết quả luôn {short_url, ending, key_item, is_exist: 1}.
func (s *Shortener) UpdateMulti(ctx context.Context, u *domain.User, items []UpdateItem, campaignCode string, campaignID int64, ip string) ([]obj, error) {
	out := make([]obj, len(items))
	for i, it := range items {
		longURL := strings.TrimSpace(it.Path)
		k := it.KeyItem
		if k == nil {
			k = ""
		}
		out[i] = o("short_url", s.FormatLink(it.Custom, s.cfg.DefaultPrefix), "ending", it.Custom, "key_item", k, "is_exist", 1)
		if it.Custom == "" {
			continue // hệ cũ ghi bản ghi short_url rỗng — không tạo bản ghi rác, output giữ nguyên
		}
		nl := s.newLink(u, longURL, it.Custom, true, domain.APIv2, s.cfg.DefaultPrefix, campaignCode, campaignID, ip, 0)
		var expires any
		if nl.ExpiresAt != nil {
			expires = *nl.ExpiresAt
		}
		set := append(s.ctvSet(longURL),
			bson.E{Key: "campaign_code", Value: campaignCode}, bson.E{Key: "campaign_id", Value: campaignID},
			bson.E{Key: "owner_username", Value: u.Username}, bson.E{Key: "owner_id", Value: u.ID},
			bson.E{Key: "is_custom", Value: true}, bson.E{Key: "is_api", Value: true}, bson.E{Key: "ip", Value: ip},
			bson.E{Key: "expires_at", Value: expires})
		l, err := s.st.UpdateLongURL(ctx, it.Custom, longURL, set)
		if err == nil {
			s.changed(ctx, events.LinkUpdated, l)
			continue
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if err := s.st.InsertLink(ctx, nl); err != nil {
			if !store.IsDuplicate(err) {
				return nil, err
			}
			if l, uerr := s.st.UpdateLongURL(ctx, it.Custom, longURL, set); uerr == nil {
				s.changed(ctx, events.LinkUpdated, l)
			}
			continue
		}
		s.created(ctx, u, nl)
	}
	return out, nil
}

// ---------- delete / restore ----------

// SetDeleted: xoá mềm / khôi phục theo mã (LinkService::deleteLink — không kiểm chủ, giữ như hệ cũ).
func (s *Shortener) SetDeleted(ctx context.Context, shortURL string, del bool) error {
	code := lastSegment(shortURL)
	from, to := []string{domain.StatusActive, domain.StatusDisabled}, domain.StatusDeleted
	typ := events.LinkDeleted
	if !del {
		from, to, typ = []string{domain.StatusDeleted}, domain.StatusActive, events.LinkRestore
	}
	l, err := s.st.SetStatus(ctx, code, "", from, to)
	if errors.Is(err, store.ErrNotFound) {
		return errData("Link rút gọn không tồn tại", nil)
	}
	if err != nil {
		return errData("Xóa thất bại!", nil)
	}
	s.changed(ctx, typ, l)
	return nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if strings.EqualFold(strings.TrimSpace(v), x) {
			return true
		}
	}
	return false
}
