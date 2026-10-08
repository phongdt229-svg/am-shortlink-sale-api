package store

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"am-shortlink-service/internal/domain"
)

// LongURLHash: sha1 hex của long_url (dedupe dùng hash + so khớp long_url đầy đủ, D5).
func LongURLHash(u string) string {
	h := sha1.Sum([]byte(u))
	return hex.EncodeToString(h[:])
}

// LinkByCode tra link theo mã (mọi trạng thái).
func (s *Store) LinkByCode(ctx context.Context, code string) (*domain.Link, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	var l domain.Link
	if err := s.core(CollLinks).FindOne(ctx, bson.D{{Key: "code", Value: code}}).Decode(&l); err != nil {
		return nil, notFound(err)
	}
	return &l, nil
}

// LinksByCodes: code → link cho danh sách mã.
func (s *Store) LinksByCodes(ctx context.Context, codes []string) (map[string]*domain.Link, error) {
	out := map[string]*domain.Link{}
	if len(codes) == 0 {
		return out, nil
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	cur, err := s.core(CollLinks).Find(ctx, bson.D{{Key: "code", Value: bson.D{{Key: "$in", Value: codes}}}})
	if err != nil {
		return nil, err
	}
	var docs []domain.Link
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	for i := range docs {
		out[docs[i].Code] = &docs[i]
	}
	return out, nil
}

// OwnerLinkByLongURL: link chưa xoá của owner có đúng long_url (+ campaign nếu withCampaign),
// không phải custom — giống longLinkExists / linkExistsByCreatorAndHash cũ. Lấy bản cũ nhất.
func (s *Store) OwnerLinkByLongURL(ctx context.Context, owner, longURL string, campaign *string) (*domain.Link, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	f := bson.D{
		{Key: "owner_username", Value: owner},
		{Key: "long_url_hash", Value: LongURLHash(longURL)},
		{Key: "long_url", Value: longURL},
		{Key: "status", Value: bson.D{{Key: "$ne", Value: domain.StatusDeleted}}},
	}
	if campaign != nil {
		f = append(f, bson.E{Key: "campaign_code", Value: *campaign})
	}
	var l domain.Link
	err := s.core(CollLinks).FindOne(ctx, f, options.FindOne().SetSort(bson.D{{Key: "_id", Value: 1}})).Decode(&l)
	if err != nil {
		return nil, notFound(err)
	}
	return &l, nil
}

// OwnerNonCustomByLongURL: link KHÔNG custom của owner có đúng long_url, MỌI trạng thái (kể cả đã xoá)
// — đúng LinkHelper::longLinkExists cũ (không lọc is_deleted). Lấy bản cũ nhất.
func (s *Store) OwnerNonCustomByLongURL(ctx context.Context, owner, longURL string) (*domain.Link, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	var l domain.Link
	err := s.core(CollLinks).FindOne(ctx, bson.D{
		{Key: "owner_username", Value: owner},
		{Key: "long_url_hash", Value: LongURLHash(longURL)},
		{Key: "long_url", Value: longURL},
		{Key: "is_custom", Value: false},
	}, options.FindOne().SetSort(bson.D{{Key: "_id", Value: 1}})).Decode(&l)
	if err != nil {
		return nil, notFound(err)
	}
	return &l, nil
}

// CodesByLongURLs: long_url → code của BẤT KỲ link nào (mọi user, mọi trạng thái) — đúng
// LinkFactory::checkDuplicateUrls của v2 shorten-multi (bản ghi sau ghi đè bản trước, như pluck()).
func (s *Store) CodesByLongURLs(ctx context.Context, longURLs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(longURLs) == 0 {
		return out, nil
	}
	hashes := make([]string, 0, len(longURLs))
	seen := map[string]bool{}
	for _, u := range longURLs {
		if h := LongURLHash(u); !seen[h] {
			seen[h] = true
			hashes = append(hashes, h)
		}
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	cur, err := s.core(CollLinks).Find(ctx, bson.D{{Key: "long_url_hash", Value: bson.D{{Key: "$in", Value: hashes}}}},
		options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetProjection(bson.D{{Key: "code", Value: 1}, {Key: "long_url", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []struct {
		Code    string `bson:"code"`
		LongURL string `bson:"long_url"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	for _, d := range docs {
		out[d.LongURL] = d.Code
	}
	return out, nil
}

// OwnerLinksByLongURLs:"long_url\x00campaign_code" → code, cho link active của owner (v3 multi).
func (s *Store) OwnerLinksByLongURLs(ctx context.Context, owner string, longURLs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(longURLs) == 0 {
		return out, nil
	}
	hashes := make([]string, 0, len(longURLs))
	seen := map[string]bool{}
	for _, u := range longURLs {
		h := LongURLHash(u)
		if !seen[h] {
			seen[h] = true
			hashes = append(hashes, h)
		}
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	cur, err := s.core(CollLinks).Find(ctx, bson.D{
		{Key: "owner_username", Value: owner},
		{Key: "long_url_hash", Value: bson.D{{Key: "$in", Value: hashes}}},
		{Key: "status", Value: domain.StatusActive},
	}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).
		SetProjection(bson.D{{Key: "code", Value: 1}, {Key: "long_url", Value: 1}, {Key: "campaign_code", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []struct {
		Code     string `bson:"code"`
		LongURL  string `bson:"long_url"`
		Campaign string `bson:"campaign_code"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	for _, d := range docs {
		k := d.LongURL + "\x00" + d.Campaign
		if _, ok := out[k]; !ok {
			out[k] = d.Code
		}
	}
	return out, nil
}

// CodesTaken: mã nào trong danh sách đã tồn tại.
func (s *Store) CodesTaken(ctx context.Context, codes []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(codes) == 0 {
		return out, nil
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	cur, err := s.core(CollLinks).Find(ctx, bson.D{{Key: "code", Value: bson.D{{Key: "$in", Value: codes}}}},
		options.Find().SetProjection(bson.D{{Key: "code", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []struct {
		Code string `bson:"code"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	for _, d := range docs {
		out[d.Code] = true
	}
	return out, nil
}

// InsertLink ghi link mới (cấp _id từ counters). Trùng code → mongo duplicate key error.
func (s *Store) InsertLink(ctx context.Context, l *domain.Link) error {
	id, err := s.NextID(ctx, CollLinks, 1)
	if err != nil {
		return err
	}
	l.ID = id
	prepareLink(l)
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	_, err = s.core(CollLinks).InsertOne(ctx, l)
	return err
}

// InsertLinks ghi nhiều link (unordered). Trả về chỉ số các phần tử bị trùng code.
func (s *Store) InsertLinks(ctx context.Context, links []*domain.Link) (dupIdx map[int]bool, err error) {
	dupIdx = map[int]bool{}
	if len(links) == 0 {
		return dupIdx, nil
	}
	first, err := s.NextID(ctx, CollLinks, int64(len(links)))
	if err != nil {
		return nil, err
	}
	docs := make([]any, len(links))
	for i, l := range links {
		l.ID = first + int64(i)
		prepareLink(l)
		docs[i] = l
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	_, err = s.core(CollLinks).InsertMany(ctx, docs, options.InsertMany().SetOrdered(false))
	if err == nil {
		return dupIdx, nil
	}
	var bwe mongo.BulkWriteException
	if ok := asBulkWriteException(err, &bwe); ok && bwe.WriteConcernError == nil {
		for _, we := range bwe.WriteErrors {
			if we.Code != 11000 {
				return nil, err
			}
			dupIdx[we.Index] = true
		}
		return dupIdx, nil
	}
	return nil, err
}

func prepareLink(l *domain.Link) {
	now := time.Now().UTC()
	if l.CreatedAt.IsZero() {
		l.CreatedAt = now
	}
	l.UpdatedAt = l.CreatedAt
	l.LongURLHash = LongURLHash(l.LongURL)
	if l.Status == "" {
		l.Status = domain.StatusActive
	}
}

// UpdateLongURL đổi long_url (+campaign) của link theo code. Trả về link sau cập nhật.
func (s *Store) UpdateLongURL(ctx context.Context, code, longURL string, set bson.D) (*domain.Link, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	upd := bson.D{
		{Key: "long_url", Value: longURL},
		{Key: "long_url_hash", Value: LongURLHash(longURL)},
		{Key: "updated_at", Value: time.Now().UTC()},
	}
	upd = append(upd, set...)
	var l domain.Link
	err := s.core(CollLinks).FindOneAndUpdate(ctx, bson.D{{Key: "code", Value: code}},
		bson.D{{Key: "$set", Value: upd}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&l)
	if err != nil {
		return nil, notFound(err)
	}
	return &l, nil
}

// SetStatus đổi trạng thái link nếu đang ở trạng thái from. owner rỗng = không kiểm chủ.
func (s *Store) SetStatus(ctx context.Context, code, owner string, from []string, to string) (*domain.Link, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	f := bson.D{{Key: "code", Value: code}, {Key: "status", Value: bson.D{{Key: "$in", Value: from}}}}
	if owner != "" {
		f = append(f, bson.E{Key: "owner_username", Value: owner})
	}
	var l domain.Link
	err := s.core(CollLinks).FindOneAndUpdate(ctx, f,
		bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: to}, {Key: "updated_at", Value: time.Now().UTC()}}}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&l)
	if err != nil {
		return nil, notFound(err)
	}
	return &l, nil
}

// CountAPILinksSince: số link API owner tạo từ mốc t (quota khi không có Redis).
func (s *Store) CountAPILinksSince(ctx context.Context, owner string, t time.Time) (int64, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	return s.core(CollLinks).CountDocuments(ctx, bson.D{
		{Key: "owner_username", Value: owner},
		{Key: "created_at", Value: bson.D{{Key: "$gte", Value: t}}},
		{Key: "is_api", Value: true},
	})
}

// IncLinkClicks cộng links.clicks theo lô (link_id → n).
func (s *Store) IncLinkClicks(ctx context.Context, inc map[int64]int64) error {
	if len(inc) == 0 {
		return nil
	}
	models := make([]mongo.WriteModel, 0, len(inc))
	for id, n := range inc {
		models = append(models, mongo.NewUpdateOneModel().
			SetFilter(bson.D{{Key: "_id", Value: id}}).
			SetUpdate(bson.D{{Key: "$inc", Value: bson.D{{Key: "clicks", Value: n}}}}))
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	_, err := s.core(CollLinks).BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false))
	return err
}

// NextID cấp n id liên tiếp từ counters; trả id đầu tiên. Lần đầu khởi tạo từ max(_id) hiện có
// (dữ liệu migrate từ MySQL giữ id cũ, link mới tiếp nối).
func (s *Store) NextID(ctx context.Context, name string, n int64) (int64, error) {
	if n <= 0 {
		n = 1
	}
	if err := s.EnsureCounter(ctx, name); err != nil {
		return 0, err
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	var doc struct {
		Seq int64 `bson:"seq"`
	}
	err := s.core(CollCounters).FindOneAndUpdate(ctx, bson.D{{Key: "_id", Value: name}},
		bson.D{{Key: "$inc", Value: bson.D{{Key: "seq", Value: n}}}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&doc)
	if err != nil {
		return 0, fmt.Errorf("counters %s: %w", name, err)
	}
	return doc.Seq - n + 1, nil
}

// EnsureCounter tạo counters/{name} = max(_id) của collection nếu chưa có.
func (s *Store) EnsureCounter(ctx context.Context, name string) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	n, err := s.core(CollCounters).CountDocuments(ctx, bson.D{{Key: "_id", Value: name}})
	if err != nil || n > 0 {
		return err
	}
	var max struct {
		ID int64 `bson:"_id"`
	}
	err = s.core(name).FindOne(ctx, bson.D{}, options.FindOne().
		SetSort(bson.D{{Key: "_id", Value: -1}}).SetProjection(bson.D{{Key: "_id", Value: 1}})).Decode(&max)
	if err != nil && err != mongo.ErrNoDocuments {
		return fmt.Errorf("max _id %s: %w", name, err)
	}
	_, err = s.core(CollCounters).UpdateOne(ctx, bson.D{{Key: "_id", Value: name}},
		bson.D{{Key: "$setOnInsert", Value: bson.D{{Key: "seq", Value: max.ID}}}}, options.UpdateOne().SetUpsert(true))
	return err
}
