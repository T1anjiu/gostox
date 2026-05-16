package cache

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	gostox "github.com/T1anjiu/gostox"
)

type mockProvider struct {
	name string
	q    func(codes ...gostox.StockCode) ([]*gostox.Quote, error)
	k    func(code gostox.StockCode, period gostox.KlinePeriod, count int) ([]*gostox.Kline, error)
	sl   func() ([]*gostox.StockInfo, error)
	iq   func(codes ...gostox.IndexCode) ([]*gostox.IndexQuote, error)
	ik   func(code gostox.IndexCode, period gostox.KlinePeriod, count int) ([]*gostox.IndexKline, error)
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) GetQuote(ctx context.Context, codes ...gostox.StockCode) ([]*gostox.Quote, error) {
	return m.q(codes...)
}
func (m *mockProvider) GetKline(ctx context.Context, code gostox.StockCode, period gostox.KlinePeriod, count int) ([]*gostox.Kline, error) {
	return m.k(code, period, count)
}
func (m *mockProvider) GetStockList(ctx context.Context) ([]*gostox.StockInfo, error) {
	return m.sl()
}
func (m *mockProvider) GetIndexQuote(ctx context.Context, codes ...gostox.IndexCode) ([]*gostox.IndexQuote, error) {
	return m.iq(codes...)
}
func (m *mockProvider) GetIndexKline(ctx context.Context, code gostox.IndexCode, period gostox.KlinePeriod, count int) ([]*gostox.IndexKline, error) {
	return m.ik(code, period, count)
}

func TestCache_Quote_HitsCacheOnSecondCall(t *testing.T) {
	var callCount atomic.Int32
	inner := &mockProvider{
		name: "mock",
		q: func(codes ...gostox.StockCode) ([]*gostox.Quote, error) {
			callCount.Add(1)
			return []*gostox.Quote{{Name: "test", Code: codes[0]}}, nil
		},
	}

	p := New(inner, DefaultTTL)
	code := gostox.StockCode{Market: gostox.MarketSH, Code: "600000"}

	got1, err := p.GetQuote(context.Background(), code)
	if err != nil || len(got1) != 1 || got1[0].Name != "test" {
		t.Fatalf("first call: %v, %+v", err, got1)
	}

	got2, err := p.GetQuote(context.Background(), code)
	if err != nil || len(got2) != 1 || got2[0].Name != "test" {
		t.Fatalf("second call: %v, %+v", err, got2)
	}

	if n := callCount.Load(); n != 1 {
		t.Fatalf("expected 1 inner call, got %d", n)
	}
}

func TestCache_Quote_Expires(t *testing.T) {
	var callCount atomic.Int32
	inner := &mockProvider{
		name: "mock",
		q: func(codes ...gostox.StockCode) ([]*gostox.Quote, error) {
			callCount.Add(1)
			return []*gostox.Quote{{Name: "test", Code: codes[0]}}, nil
		},
	}

	ttl := DefaultTTL
	ttl.Quote = 50 * time.Millisecond
	p := New(inner, ttl)
	code := gostox.StockCode{Market: gostox.MarketSH, Code: "600000"}

	p.GetQuote(context.Background(), code)
	time.Sleep(100 * time.Millisecond)
	p.GetQuote(context.Background(), code)

	if n := callCount.Load(); n != 2 {
		t.Fatalf("expected 2 inner calls after expiry, got %d", n)
	}
}

func TestCache_Kline_HitsCache(t *testing.T) {
	var callCount atomic.Int32
	inner := &mockProvider{
		name: "mock",
		k: func(code gostox.StockCode, period gostox.KlinePeriod, count int) ([]*gostox.Kline, error) {
			callCount.Add(1)
			return []*gostox.Kline{{Open: 10.0, Code: code, Period: period}}, nil
		},
	}

	p := New(inner, DefaultTTL)
	code := gostox.StockCode{Market: gostox.MarketSH, Code: "600000"}

	p.GetKline(context.Background(), code, gostox.KlinePeriodDay, 5)
	p.GetKline(context.Background(), code, gostox.KlinePeriodDay, 5)

	if n := callCount.Load(); n != 1 {
		t.Fatalf("expected 1 inner call, got %d", n)
	}
}

func TestCache_Kline_DifferentParams_NoCacheHit(t *testing.T) {
	var callCount atomic.Int32
	inner := &mockProvider{
		name: "mock",
		k: func(code gostox.StockCode, period gostox.KlinePeriod, count int) ([]*gostox.Kline, error) {
			callCount.Add(1)
			return []*gostox.Kline{{Open: 10.0, Code: code, Period: period}}, nil
		},
	}

	p := New(inner, DefaultTTL)
	code := gostox.StockCode{Market: gostox.MarketSH, Code: "600000"}

	p.GetKline(context.Background(), code, gostox.KlinePeriodDay, 5)
	p.GetKline(context.Background(), code, gostox.KlinePeriodDay, 10)
	p.GetKline(context.Background(), code, gostox.KlinePeriodWeek, 5)

	if n := callCount.Load(); n != 3 {
		t.Fatalf("expected 3 inner calls for different params, got %d", n)
	}
}

func TestCache_StockList(t *testing.T) {
	var callCount atomic.Int32
	inner := &mockProvider{
		name: "mock",
		sl: func() ([]*gostox.StockInfo, error) {
			callCount.Add(1)
			return []*gostox.StockInfo{{Name: "test"}}, nil
		},
	}

	p := New(inner, DefaultTTL)

	p.GetStockList(context.Background())
	p.GetStockList(context.Background())

	if n := callCount.Load(); n != 1 {
		t.Fatalf("expected 1 inner call, got %d", n)
	}
}

func TestCache_PartialHit_Quote(t *testing.T) {
	var callCount atomic.Int32
	inner := &mockProvider{
		name: "mock",
		q: func(codes ...gostox.StockCode) ([]*gostox.Quote, error) {
			callCount.Add(1)
			var out []*gostox.Quote
			for _, c := range codes {
				out = append(out, &gostox.Quote{Name: c.String(), Code: c})
			}
			return out, nil
		},
	}

	p := New(inner, DefaultTTL)
	c1 := gostox.StockCode{Market: gostox.MarketSH, Code: "600000"}
	c2 := gostox.StockCode{Market: gostox.MarketSZ, Code: "000001"}

	p.GetQuote(context.Background(), c1)
	p.GetQuote(context.Background(), c1, c2)

	if n := callCount.Load(); n != 2 {
		t.Fatalf("expected 2 inner calls (first 1, second partial for missing 1), got %d", n)
	}
}

func TestCache_IndexQuoteAndKline(t *testing.T) {
	var qc, kc atomic.Int32
	inner := &mockProvider{
		name: "mock",
		iq: func(codes ...gostox.IndexCode) ([]*gostox.IndexQuote, error) {
			qc.Add(1)
			var out []*gostox.IndexQuote
			for _, c := range codes {
				out = append(out, &gostox.IndexQuote{Name: c.String(), Code: c})
			}
			return out, nil
		},
		ik: func(code gostox.IndexCode, period gostox.KlinePeriod, count int) ([]*gostox.IndexKline, error) {
			kc.Add(1)
			return []*gostox.IndexKline{{Open: 3000, Code: code}}, nil
		},
	}

	p := New(inner, DefaultTTL)
	ic := gostox.IndexCode{Code: "000001"}

	p.GetIndexQuote(context.Background(), ic)
	p.GetIndexQuote(context.Background(), ic)
	p.GetIndexKline(context.Background(), ic, gostox.KlinePeriodDay, 5)
	p.GetIndexKline(context.Background(), ic, gostox.KlinePeriodDay, 5)

	if n := qc.Load(); n != 1 {
		t.Fatalf("expected 1 inner call for index quote, got %d", n)
	}
	if n := kc.Load(); n != 1 {
		t.Fatalf("expected 1 inner call for index kline, got %d", n)
	}
}

func TestCache_EmptyCodes(t *testing.T) {
	var called bool
	inner := &mockProvider{
		name: "mock",
		q: func(codes ...gostox.StockCode) ([]*gostox.Quote, error) {
			called = true
			return nil, nil
		},
		iq: func(codes ...gostox.IndexCode) ([]*gostox.IndexQuote, error) {
			called = true
			return nil, nil
		},
	}

	p := New(inner, DefaultTTL)

	p.GetQuote(context.Background())
	if called {
		t.Error("GetQuote with empty codes should not call inner")
	}

	p.GetIndexQuote(context.Background())
	if called {
		t.Error("GetIndexQuote with empty codes should not call inner")
	}
}

func TestCache_Error(t *testing.T) {
	wantErr := errors.New("inner error")
	inner := &mockProvider{
		name: "mock",
		k: func(code gostox.StockCode, period gostox.KlinePeriod, count int) ([]*gostox.Kline, error) {
			return nil, wantErr
		},
	}

	p := New(inner, DefaultTTL)
	code := gostox.StockCode{Market: gostox.MarketSH, Code: "600000"}

	_, err := p.GetKline(context.Background(), code, gostox.KlinePeriodDay, 5)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
}

func TestCache_Quote_PartialErrorPropagatedAndDataCached(t *testing.T) {
	var callCount atomic.Int32
	inner := &mockProvider{
		name: "mock",
		q: func(codes ...gostox.StockCode) ([]*gostox.Quote, error) {
			callCount.Add(1)
			// 只返回第一只，其它视为部分失败
			if len(codes) == 0 {
				return nil, nil
			}
			return []*gostox.Quote{{Name: codes[0].String(), Code: codes[0]}},
				&gostox.PartialError{Failures: []error{errors.New("missing")}}
		},
	}

	p := New(inner, DefaultTTL)
	c1 := gostox.StockCode{Market: gostox.MarketSH, Code: "600000"}
	c2 := gostox.StockCode{Market: gostox.MarketSZ, Code: "000001"}

	got, err := p.GetQuote(context.Background(), c1, c2)
	var pe *gostox.PartialError
	if !errors.As(err, &pe) {
		t.Fatalf("want PartialError, got %v", err)
	}
	if len(got) != 1 || got[0].Code != c1 {
		t.Fatalf("unexpected quotes: %+v", got)
	}

	// 第二次只请求已成功缓存的 c1，应不再调用 inner
	got2, err2 := p.GetQuote(context.Background(), c1)
	if err2 != nil {
		t.Fatalf("unexpected err: %v", err2)
	}
	if len(got2) != 1 {
		t.Fatalf("cache miss: %+v", got2)
	}
	if n := callCount.Load(); n != 1 {
		t.Fatalf("expected 1 inner call (c1 should be cached despite PartialError), got %d", n)
	}
}

func TestCache_IndexQuote_PartialErrorPropagatedAndDataCached(t *testing.T) {
	var callCount atomic.Int32
	inner := &mockProvider{
		name: "mock",
		iq: func(codes ...gostox.IndexCode) ([]*gostox.IndexQuote, error) {
			callCount.Add(1)
			if len(codes) == 0 {
				return nil, nil
			}
			return []*gostox.IndexQuote{{Name: codes[0].String(), Code: codes[0]}},
				&gostox.PartialError{Failures: []error{errors.New("missing")}}
		},
	}

	p := New(inner, DefaultTTL)
	i1 := gostox.IndexCode{Code: "000001"}
	i2 := gostox.IndexCode{Code: "399001"}

	got, err := p.GetIndexQuote(context.Background(), i1, i2)
	var pe *gostox.PartialError
	if !errors.As(err, &pe) {
		t.Fatalf("want PartialError, got %v", err)
	}
	if len(got) != 1 || got[0].Code != i1 {
		t.Fatalf("unexpected quotes: %+v", got)
	}

	got2, err2 := p.GetIndexQuote(context.Background(), i1)
	if err2 != nil {
		t.Fatalf("unexpected err: %v", err2)
	}
	if len(got2) != 1 {
		t.Fatalf("cache miss: %+v", got2)
	}
	if n := callCount.Load(); n != 1 {
		t.Fatalf("expected 1 inner call, got %d", n)
	}
}
