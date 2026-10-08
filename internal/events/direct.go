package events

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Direct: hàng đợi trong tiến trình → Handler, gom click theo lô (tối đa 500 hoặc 200ms).
type Direct struct {
	h       Handler
	log     *slog.Logger
	clicks  chan ClickEvent
	links   chan LinkEvent
	wg      sync.WaitGroup
	dropped atomic.Int64
	stop    chan struct{}
}

func NewDirect(h Handler, log *slog.Logger, buffer int) *Direct {
	if buffer <= 0 {
		buffer = 10000
	}
	d := &Direct{h: h, log: log, clicks: make(chan ClickEvent, buffer), links: make(chan LinkEvent, 1000), stop: make(chan struct{})}
	d.wg.Add(2)
	go d.loopClicks()
	go d.loopLinks()
	return d
}

func (d *Direct) PublishClick(_ context.Context, e ClickEvent) {
	select {
	case d.clicks <- e:
	default:
		if n := d.dropped.Add(1); n%1000 == 1 {
			d.log.Warn("click queue full, dropping", "dropped_total", n)
		}
	}
}

func (d *Direct) PublishLink(_ context.Context, e LinkEvent) {
	select {
	case d.links <- e:
	default:
		d.log.Warn("link queue full, dropping", "code", e.Link.Code, "type", e.Type)
	}
}

func (d *Direct) loopClicks() {
	defer d.wg.Done()
	batch := make([]ClickEvent, 0, 500)
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := d.h.HandleClicks(ctx, batch); err != nil {
			d.log.Error("handle clicks", "err", err, "n", len(batch))
		}
		cancel()
		batch = batch[:0]
	}
	for {
		select {
		case e := <-d.clicks:
			batch = append(batch, e)
			if len(batch) >= 500 {
				flush()
			}
		case <-t.C:
			flush()
		case <-d.stop:
			for {
				select {
				case e := <-d.clicks:
					batch = append(batch, e)
				default:
					flush()
					return
				}
			}
		}
	}
}

func (d *Direct) loopLinks() {
	defer d.wg.Done()
	handle := func(e LinkEvent) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := d.h.HandleLink(ctx, e); err != nil {
			d.log.Error("handle link event", "err", err, "code", e.Link.Code)
		}
	}
	for {
		select {
		case e := <-d.links:
			handle(e)
		case <-d.stop:
			for {
				select {
				case e := <-d.links:
					handle(e)
				default:
					return
				}
			}
		}
	}
}

// Close xả hết hàng đợi rồi dừng.
func (d *Direct) Close(ctx context.Context) error {
	close(d.stop)
	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Nop: chỉ đếm, không xử lý (CLICK_SINK=log).
type Nop struct{ Log *slog.Logger }

func (n Nop) PublishClick(_ context.Context, e ClickEvent) {
	n.Log.Debug("click", "code", e.Link.Code, "event_id", e.EventID)
}
func (n Nop) PublishLink(_ context.Context, e LinkEvent) {
	n.Log.Debug("link event", "code", e.Link.Code, "type", e.Type)
}
func (Nop) Close(context.Context) error { return nil }
