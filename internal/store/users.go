package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"am-shortlink-service/internal/domain"
)

// HashAPIKey: SHA-256 hex của key gốc (key ngẫu nhiên đủ dài → không cần bcrypt, tra được theo index).
func HashAPIKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

func (s *Store) UserByID(ctx context.Context, id int64) (*domain.User, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	var u domain.User
	if err := s.core(CollUsers).FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&u); err != nil {
		return nil, notFound(err)
	}
	return &u, nil
}

func (s *Store) UserByUsername(ctx context.Context, username string) (*domain.User, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	var u domain.User
	if err := s.core(CollUsers).FindOne(ctx, bson.D{{Key: "username", Value: username}}).Decode(&u); err != nil {
		return nil, notFound(err)
	}
	return &u, nil
}

// UserByAPIKey: key gốc → user (api_keys.active). Không kiểm active/api_active — middleware kiểm.
func (s *Store) UserByAPIKey(ctx context.Context, key string) (*domain.User, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	var k domain.APIKey
	err := s.core(CollAPIKeys).FindOne(ctx, bson.D{{Key: "key_hash", Value: HashAPIKey(key)}, {Key: "active", Value: true}}).Decode(&k)
	if err != nil {
		return nil, notFound(err)
	}
	var u domain.User
	if err := s.core(CollUsers).FindOne(ctx, bson.D{{Key: "_id", Value: k.UserID}}).Decode(&u); err != nil {
		return nil, notFound(err)
	}
	return &u, nil
}

// TouchAPIKey cập nhật last_used_at (gọi bất đồng bộ, lỗi bỏ qua).
func (s *Store) TouchAPIKey(ctx context.Context, key string) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	_, _ = s.core(CollAPIKeys).UpdateOne(ctx, bson.D{{Key: "key_hash", Value: HashAPIKey(key)}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "last_used_at", Value: time.Now().UTC()}}}})
}

// RotateAPIKey: vô hiệu mọi key cũ của user, ghi key mới (hash).
func (s *Store) RotateAPIKey(ctx context.Context, userID int64, key string) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	now := time.Now().UTC()
	if _, err := s.core(CollAPIKeys).UpdateMany(ctx, bson.D{{Key: "user_id", Value: userID}, {Key: "active", Value: true}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "active", Value: false}, {Key: "rotated_at", Value: now}}}}); err != nil {
		return err
	}
	_, err := s.core(CollAPIKeys).InsertOne(ctx, domain.APIKey{UserID: userID, KeyHash: HashAPIKey(key), Active: true, CreatedAt: now})
	return err
}

// ImportAPIKey ghi key đã có (migrate từ users.api_key MySQL) nếu chưa tồn tại.
func (s *Store) ImportAPIKey(ctx context.Context, userID int64, key string) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	h := HashAPIKey(key)
	_, err := s.core(CollAPIKeys).UpdateOne(ctx, bson.D{{Key: "key_hash", Value: h}},
		bson.D{{Key: "$setOnInsert", Value: bson.D{
			{Key: "user_id", Value: userID}, {Key: "key_hash", Value: h}, {Key: "active", Value: true},
			{Key: "created_at", Value: time.Now().UTC()},
		}}}, options.UpdateOne().SetUpsert(true))
	return err
}

func (s *Store) HasActiveAPIKey(ctx context.Context, userID int64) (bool, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	n, err := s.core(CollAPIKeys).CountDocuments(ctx, bson.D{{Key: "user_id", Value: userID}, {Key: "active", Value: true}})
	return n > 0, err
}

// CreateUser tạo user (id từ counters).
func (s *Store) CreateUser(ctx context.Context, u *domain.User) error {
	id, err := s.NextID(ctx, CollUsers, 1)
	if err != nil {
		return err
	}
	u.ID = id
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC()
	}
	if u.ViewerAccounts == nil {
		u.ViewerAccounts = []string{}
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	_, err = s.core(CollUsers).InsertOne(ctx, u)
	return err
}

// UpdateUser đặt các trường.
func (s *Store) UpdateUser(ctx context.Context, id int64, set bson.D) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	_, err := s.core(CollUsers).UpdateOne(ctx, bson.D{{Key: "_id", Value: id}}, bson.D{{Key: "$set", Value: set}})
	return err
}
