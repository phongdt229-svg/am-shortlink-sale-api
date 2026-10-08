package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// KafkaPublisher phát sự kiện bất đồng bộ (không chờ ack trên đường nóng).
// Key = link_id → mọi sự kiện của một link vào cùng partition (giữ thứ tự created → clicks).
type KafkaPublisher struct {
	cl         *kgo.Client
	log        *slog.Logger
	clickTopic string
	linkTopic  string
	failed     atomic.Int64
}

func NewKafkaPublisher(brokers []string, clickTopic, linkTopic string, log *slog.Logger) (*KafkaPublisher, error) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ProducerLinger(20*time.Millisecond),
		kgo.ProducerBatchCompression(kgo.Lz4Compression()),
		kgo.RecordDeliveryTimeout(30*time.Second),
		kgo.MaxBufferedRecords(100000),
	)
	if err != nil {
		return nil, fmt.Errorf("kafka producer: %w", err)
	}
	return &KafkaPublisher{cl: cl, log: log, clickTopic: clickTopic, linkTopic: linkTopic}, nil
}

func (p *KafkaPublisher) produce(topic string, key int64, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		p.log.Error("marshal event", "err", err)
		return
	}
	rec := &kgo.Record{Topic: topic, Key: []byte(strconv.FormatInt(key, 10)), Value: b}
	// TryProduce: buffer đầy → lỗi ngay thay vì chặn redirect.
	p.cl.TryProduce(context.Background(), rec, func(_ *kgo.Record, err error) {
		if err != nil {
			if n := p.failed.Add(1); n%1000 == 1 {
				p.log.Error("kafka produce failed", "topic", topic, "err", err, "failed_total", n)
			}
		}
	})
}

func (p *KafkaPublisher) PublishClick(_ context.Context, e ClickEvent) {
	p.produce(p.clickTopic, e.Link.ID, e)
}

func (p *KafkaPublisher) PublishLink(_ context.Context, e LinkEvent) {
	p.produce(p.linkTopic, e.Link.ID, e)
}

func (p *KafkaPublisher) Close(ctx context.Context) error {
	err := p.cl.Flush(ctx)
	p.cl.Close()
	return err
}

// RunKafkaConsumer đọc topic click + link theo consumer group, gọi Handler theo lô,
// commit offset SAU khi xử lý xong (at-least-once; Handler idempotent theo event_id).
func RunKafkaConsumer(ctx context.Context, brokers []string, group, clickTopic, linkTopic string, h Handler, log *slog.Logger) error {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(clickTopic, linkTopic),
		kgo.DisableAutoCommit(),
		kgo.FetchMaxWait(500*time.Millisecond),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		return fmt.Errorf("kafka consumer: %w", err)
	}
	defer cl.Close()
	log.Info("kafka consumer started", "group", group, "topics", []string{clickTopic, linkTopic})

	for {
		fetches := cl.PollRecords(ctx, 1000)
		if ctx.Err() != nil {
			return nil
		}
		fetches.EachError(func(t string, p int32, err error) {
			log.Error("kafka fetch", "topic", t, "partition", p, "err", err)
		})
		var clicks []ClickEvent
		var recs []*kgo.Record
		fetches.EachRecord(func(r *kgo.Record) {
			recs = append(recs, r)
			switch r.Topic {
			case clickTopic:
				var e ClickEvent
				if err := json.Unmarshal(r.Value, &e); err != nil {
					log.Error("bad click event", "err", err, "offset", r.Offset)
					return
				}
				clicks = append(clicks, e)
			case linkTopic:
				// Sự kiện link xử lý trước click cùng lô (link mới phải có link_params trước khi đếm param).
				var e LinkEvent
				if err := json.Unmarshal(r.Value, &e); err != nil {
					log.Error("bad link event", "err", err, "offset", r.Offset)
					return
				}
				if err := retry(ctx, func() error { return h.HandleLink(ctx, e) }); err != nil {
					log.Error("handle link event", "err", err, "code", e.Link.Code)
				}
			}
		})
		if len(clicks) > 0 {
			if err := retry(ctx, func() error { return h.HandleClicks(ctx, clicks) }); err != nil {
				// Không commit → lô được đọc lại sau khi khởi động lại (idempotent).
				return fmt.Errorf("handle clicks: %w", err)
			}
		}
		if len(recs) > 0 {
			if err := cl.CommitRecords(ctx, recs...); err != nil {
				log.Error("kafka commit", "err", err)
			}
		}
	}
}

func retry(ctx context.Context, f func() error) error {
	var err error
	for i, d := 0, 200*time.Millisecond; i < 5; i, d = i+1, d*2 {
		if err = f(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(d):
		}
	}
	return err
}
