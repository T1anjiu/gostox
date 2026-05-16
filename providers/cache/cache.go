package cache

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	gostox "github.com/T1anjiu/gostox"
)

// TTLConfig 各方法的缓存时长配置。
type TTLConfig struct {
	Quote      time.Duration
	Kline      time.Duration
	StockList  time.Duration
	IndexQuote time.Duration
	IndexKline time.Duration
}

// DefaultTTL 默认 TTL 配置。
var DefaultTTL = TTLConfig{
	Quote:      3 * time.Second,
	Kline:      60 * time.Second,
	StockList:  24 * time.Hour,
	IndexQuote: 3 * time.Second,
	IndexKline: 60 * time.Second,
}

type entry[T any] struct {
	value   T
	err     error
	expires time.Time
}

func (e *entry[T]) valid() bool {
	return time.Now().Before(e.expires)
}

// Provider 是带 TTL 缓存的 gostox.Provider 包装器。
type Provider struct {
	inner  gostox.Provider
	ttl    TTLConfig
	mu     sync.RWMutex
	quotes map[string]*entry[[]*gostox.Quote]
	klines map[string]*entry[[]*gostox.Kline]
	sl     *entry[[]*gostox.StockInfo]
	iq     map[string]*entry[[]*gostox.IndexQuote]
	ik     map[string]*entry[[]*gostox.IndexKline]
}

// New 创建缓存 Provider，包装 inner。
func New(inner gostox.Provider, ttl TTLConfig) *Provider {
	return &Provider{
		inner:  inner,
		ttl:    ttl,
		quotes: make(map[string]*entry[[]*gostox.Quote]),
		klines: make(map[string]*entry[[]*gostox.Kline]),
		iq:     make(map[string]*entry[[]*gostox.IndexQuote]),
		ik:     make(map[string]*entry[[]*gostox.IndexKline]),
	}
}

func (p *Provider) Name() string {
	return "cache(" + p.inner.Name() + ")"
}

func quoteKey(code gostox.StockCode) string {
	return "q:" + code.String()
}

func klineKey(code gostox.StockCode, period gostox.KlinePeriod, count int) string {
	return "k:" + code.String() + ":" + strconv.Itoa(int(period)) + ":" + strconv.Itoa(count)
}

func indexQuoteKey(code gostox.IndexCode) string {
	return "iq:" + code.String()
}

func indexKlineKey(code gostox.IndexCode, period gostox.KlinePeriod, count int) string {
	return "ik:" + code.String() + ":" + strconv.Itoa(int(period)) + ":" + strconv.Itoa(count)
}

func (p *Provider) GetQuote(ctx context.Context, codes ...gostox.StockCode) ([]*gostox.Quote, error) {
	if len(codes) == 0 {
		return nil, nil
	}

	p.mu.RLock()
	allCached := true
	results := make([]*gostox.Quote, 0, len(codes))
	var missing []gostox.StockCode
	for _, c := range codes {
		e, ok := p.quotes[quoteKey(c)]
		if ok && e.valid() {
			results = append(results, e.value...)
		} else {
			allCached = false
			missing = append(missing, c)
		}
	}
	p.mu.RUnlock()

	if allCached {
		return results, nil
	}

	fresh, err := p.inner.GetQuote(ctx, missing...)
	// 即使上游返回 PartialError，仍把已成功的数据写入缓存并返回。
	// 其它类型错误直接抛出，不污染缓存。
	var partialErr *gostox.PartialError
	if err != nil && !errors.As(err, &partialErr) {
		return nil, err
	}

	p.mu.Lock()
	expires := time.Now().Add(p.ttl.Quote)
	for _, q := range fresh {
		p.quotes[quoteKey(q.Code)] = &entry[[]*gostox.Quote]{value: []*gostox.Quote{q}, expires: expires}
		results = append(results, q)
	}
	p.mu.Unlock()

	return results, err
}

func (p *Provider) GetKline(ctx context.Context, code gostox.StockCode, period gostox.KlinePeriod, count int) ([]*gostox.Kline, error) {
	key := klineKey(code, period, count)

	p.mu.RLock()
	e, ok := p.klines[key]
	p.mu.RUnlock()
	if ok && e.valid() {
		return e.value, e.err
	}

	klines, err := p.inner.GetKline(ctx, code, period, count)
	var partialErr *gostox.PartialError
	if err != nil && !errors.As(err, &partialErr) {
		return klines, err
	}

	p.mu.Lock()
	p.klines[key] = &entry[[]*gostox.Kline]{value: klines, err: err, expires: time.Now().Add(p.ttl.Kline)}
	p.mu.Unlock()

	return klines, err
}

func (p *Provider) GetStockList(ctx context.Context) ([]*gostox.StockInfo, error) {
	p.mu.RLock()
	e := p.sl
	p.mu.RUnlock()
	if e != nil && e.valid() {
		return e.value, e.err
	}

	list, err := p.inner.GetStockList(ctx)
	var partialErr *gostox.PartialError
	if err != nil && !errors.As(err, &partialErr) {
		return list, err
	}

	p.mu.Lock()
	p.sl = &entry[[]*gostox.StockInfo]{value: list, err: err, expires: time.Now().Add(p.ttl.StockList)}
	p.mu.Unlock()

	return list, err
}

func (p *Provider) GetIndexQuote(ctx context.Context, codes ...gostox.IndexCode) ([]*gostox.IndexQuote, error) {
	if len(codes) == 0 {
		return nil, nil
	}

	p.mu.RLock()
	allCached := true
	results := make([]*gostox.IndexQuote, 0, len(codes))
	var missing []gostox.IndexCode
	for _, c := range codes {
		e, ok := p.iq[indexQuoteKey(c)]
		if ok && e.valid() {
			results = append(results, e.value...)
		} else {
			allCached = false
			missing = append(missing, c)
		}
	}
	p.mu.RUnlock()

	if allCached {
		return results, nil
	}

	fresh, err := p.inner.GetIndexQuote(ctx, missing...)
	// 同 GetQuote：PartialError 不阻止缓存写入，其它错误直接抛出。
	var partialErr *gostox.PartialError
	if err != nil && !errors.As(err, &partialErr) {
		return nil, err
	}

	p.mu.Lock()
	expires := time.Now().Add(p.ttl.IndexQuote)
	for _, q := range fresh {
		p.iq[indexQuoteKey(q.Code)] = &entry[[]*gostox.IndexQuote]{value: []*gostox.IndexQuote{q}, expires: expires}
		results = append(results, q)
	}
	p.mu.Unlock()

	return results, err
}

func (p *Provider) GetIndexKline(ctx context.Context, code gostox.IndexCode, period gostox.KlinePeriod, count int) ([]*gostox.IndexKline, error) {
	key := indexKlineKey(code, period, count)

	p.mu.RLock()
	e, ok := p.ik[key]
	p.mu.RUnlock()
	if ok && e.valid() {
		return e.value, e.err
	}

	klines, err := p.inner.GetIndexKline(ctx, code, period, count)
	var partialErr *gostox.PartialError
	if err != nil && !errors.As(err, &partialErr) {
		return klines, err
	}

	p.mu.Lock()
	p.ik[key] = &entry[[]*gostox.IndexKline]{value: klines, err: err, expires: time.Now().Add(p.ttl.IndexKline)}
	p.mu.Unlock()

	return klines, err
}

var _ gostox.Provider = (*Provider)(nil)
