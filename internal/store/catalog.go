package store

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"am-shortlink-service/internal/domain"
)

// ---------- campaigns ----------

func (s *Store) CampaignByCode(ctx context.Context, code string) (*domain.Campaign, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	var c domain.Campaign
	if err := s.core(CollCampaigns).FindOne(ctx, bson.D{{Key: "code", Value: code}}).Decode(&c); err != nil {
		return nil, notFound(err)
	}
	return &c, nil
}

func (s *Store) CampaignsByCodes(ctx context.Context, codes []string) ([]domain.Campaign, error) {
	if len(codes) == 0 {
		return nil, nil
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	cur, err := s.core(CollCampaigns).Find(ctx, bson.D{{Key: "code", Value: bson.D{{Key: "$in", Value: codes}}}})
	if err != nil {
		return nil, err
	}
	var out []domain.Campaign
	return out, cur.All(ctx, &out)
}

func (s *Store) CampaignNameExists(ctx context.Context, name string) (bool, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	n, err := s.core(CollCampaigns).CountDocuments(ctx, bson.D{{Key: "name", Value: name}})
	return n > 0, err
}

type CampaignFilter struct {
	CreatedBy int64
	Name      string // chứa (không phân biệt hoa thường)
	Code      string
	Page      int64
	PerPage   int64
}

// ListCampaigns: danh sách chiến dịch của user, mới nhất trước, có tổng.
func (s *Store) ListCampaigns(ctx context.Context, f CampaignFilter) ([]domain.Campaign, int64, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	q := bson.D{{Key: "created_by", Value: f.CreatedBy}}
	if f.Name != "" {
		q = append(q, bson.E{Key: "name", Value: bson.D{{Key: "$regex", Value: regexQuote(f.Name)}, {Key: "$options", Value: "i"}}})
	}
	if f.Code != "" {
		q = append(q, bson.E{Key: "code", Value: bson.D{{Key: "$regex", Value: regexQuote(f.Code)}, {Key: "$options", Value: "i"}}})
	}
	total, err := s.core(CollCampaigns).CountDocuments(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	cur, err := s.core(CollCampaigns).Find(ctx, q, options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetSkip((f.Page-1)*f.PerPage).SetLimit(f.PerPage))
	if err != nil {
		return nil, 0, err
	}
	var out []domain.Campaign
	return out, total, cur.All(ctx, &out)
}

// CreateCampaign tạo chiến dịch; created_by = users._id (như MySQL).
func (s *Store) CreateCampaign(ctx context.Context, name, code string, createdBy int64) (id int64, at time.Time, err error) {
	id, err = s.NextID(ctx, CollCampaigns, 1)
	if err != nil {
		return 0, at, err
	}
	at = time.Now().UTC()
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	_, err = s.core(CollCampaigns).InsertOne(ctx, bson.D{
		{Key: "_id", Value: id}, {Key: "name", Value: name}, {Key: "code", Value: code},
		{Key: "created_by", Value: createdBy}, {Key: "created_at", Value: at}, {Key: "updated_at", Value: at},
	})
	return id, at, err
}

// ---------- templates ----------

func (s *Store) ActiveTemplates(ctx context.Context) ([]domain.Template, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	cur, err := s.core(CollTemplates).Find(ctx, bson.D{{Key: "status", Value: 1}},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}))
	if err != nil {
		return nil, err
	}
	out := []domain.Template{}
	return out, cur.All(ctx, &out)
}

func (s *Store) TemplateByURL(ctx context.Context, u string) (*domain.Template, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	var t domain.Template
	if err := s.core(CollTemplates).FindOne(ctx, bson.D{{Key: "status", Value: 1}, {Key: "template_url", Value: u}}).Decode(&t); err != nil {
		return nil, notFound(err)
	}
	return &t, nil
}

// ---------- domains, prefixes ----------

func (s *Store) ActiveDomains(ctx context.Context) ([]domain.Domain, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	cur, err := s.core(CollDomains).Find(ctx, bson.D{{Key: "is_active", Value: true}})
	if err != nil {
		return nil, err
	}
	var out []domain.Domain
	return out, cur.All(ctx, &out)
}

func (s *Store) ActivePrefixes(ctx context.Context) ([]domain.Prefix, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	cur, err := s.core(CollPrefixes).Find(ctx, bson.D{{Key: "active", Value: true}})
	if err != nil {
		return nil, err
	}
	var out []domain.Prefix
	return out, cur.All(ctx, &out)
}

func regexQuote(s string) string {
	const special = `\.+*?()|[]{}^$`
	out := make([]rune, 0, len(s))
	for _, r := range s {
		for _, sp := range special {
			if r == sp {
				out = append(out, '\\')
				break
			}
		}
		out = append(out, r)
	}
	return string(out)
}
