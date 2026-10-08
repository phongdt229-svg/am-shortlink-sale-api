package store

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"am-shortlink-service/internal/domain"
)

// OwnerCampaignLinks: link active của owner theo campaign (POST /api/v2/report), mới nhất trước.
func (s *Store) OwnerCampaignLinks(ctx context.Context, owner, campaign string, onlyClicked bool, page, perPage int64) ([]domain.Link, int64, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	f := bson.D{{Key: "owner_username", Value: owner}, {Key: "status", Value: domain.StatusActive}}
	if campaign != "" {
		f = append(f, bson.E{Key: "campaign_code", Value: campaign})
	}
	if onlyClicked {
		f = append(f, bson.E{Key: "clicks", Value: bson.D{{Key: "$gt", Value: 0}}})
	}
	total, err := s.core(CollLinks).CountDocuments(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	cur, err := s.core(CollLinks).Find(ctx, f, options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).SetSkip((page-1)*perPage).SetLimit(perPage))
	if err != nil {
		return nil, 0, err
	}
	var out []domain.Link
	return out, total, cur.All(ctx, &out)
}

// OwnerLinksPage: link chưa xoá của owner theo trang (sắp theo _id).
func (s *Store) OwnerLinksPage(ctx context.Context, owner string, page, perPage int64) ([]domain.Link, int64, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	f := bson.D{{Key: "owner_username", Value: owner}, {Key: "status", Value: bson.D{{Key: "$ne", Value: domain.StatusDeleted}}}}
	total, err := s.core(CollLinks).CountDocuments(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	cur, err := s.core(CollLinks).Find(ctx, f, options.Find().
		SetSort(bson.D{{Key: "_id", Value: 1}}).SetSkip((page-1)*perPage).SetLimit(perPage))
	if err != nil {
		return nil, 0, err
	}
	var out []domain.Link
	return out, total, cur.All(ctx, &out)
}

// SetLinkFields đặt trường cho link theo _id.
func (s *Store) SetLinkFields(ctx context.Context, id int64, set bson.D) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	_, err := s.core(CollLinks).UpdateOne(ctx, bson.D{{Key: "_id", Value: id}}, bson.D{{Key: "$set", Value: set}})
	return err
}
