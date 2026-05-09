package gostox

import (
	"context"
	"errors"
	"testing"
)

func TestParseStockCode(t *testing.T) {
	tests := []struct {
		raw     string
		wantM   Market
		wantC   string
		wantErr bool
	}{
		{"sh600000", MarketSH, "600000", false},
		{"sz000001", MarketSZ, "000001", false},
		{"SH600000", MarketSH, "600000", false},
		{"bj430001", MarketBJ, "430001", false},
		{"x", 0, "", true},
		{"", 0, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := ParseStockCode(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.Market != tt.wantM || got.Code != tt.wantC {
				t.Fatalf("got %+v, want {%v %v}", got, tt.wantM, tt.wantC)
			}
		})
	}
}

func TestStockCodeFormatters(t *testing.T) {
	c := StockCode{Market: MarketSH, Code: "600000"}
	if c.String() != "sh600000" {
		t.Errorf("String=%q", c.String())
	}
	if c.SinaCode() != "sh600000" {
		t.Errorf("SinaCode=%q", c.SinaCode())
	}
	if c.TencentCode() != "sh600000" {
		t.Errorf("TencentCode=%q", c.TencentCode())
	}
	if c.EastmoneyCode() != "1.600000" {
		t.Errorf("EastmoneyCode=%q", c.EastmoneyCode())
	}

	c2 := StockCode{Market: MarketSZ, Code: "000001"}
	if c2.EastmoneyCode() != "0.000001" {
		t.Errorf("EastmoneyCode=%q", c2.EastmoneyCode())
	}
}

func TestInferMarket(t *testing.T) {
	cases := map[string]Market{
		"600000": MarketSH,
		"000001": MarketSZ,
		"300750": MarketSZ,
		"830949": MarketBJ,
		"430047": MarketBJ,
		"":       MarketSH, // zero value, code ""
	}
	for code, want := range cases {
		got := InferMarket(code)
		if code == "" {
			if got.Code != "" {
				t.Errorf("empty code inferred %+v", got)
			}
			continue
		}
		if got.Market != want {
			t.Errorf("InferMarket(%q).Market=%v want %v", code, got.Market, want)
		}
	}
}

// fakeProvider 用于测试 Client 的故障转移与错误聚合。
type fakeProvider struct {
	name string
	q    []*Quote
	k    []*Kline
	err  error
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) GetQuote(ctx context.Context, codes ...StockCode) ([]*Quote, error) {
	return f.q, f.err
}
func (f *fakeProvider) GetKline(ctx context.Context, code StockCode, period KlinePeriod, count int) ([]*Kline, error) {
	if f.k != nil {
		return f.k, nil
	}
	return nil, f.err
}
func (f *fakeProvider) GetStockList(ctx context.Context) ([]*StockInfo, error) {
	return nil, f.err
}

func TestClient_Failover(t *testing.T) {
	want := []*Quote{{Name: "ok"}}
	c, _ := NewClient(
		&fakeProvider{name: "p1", err: errors.New("boom")},
		&fakeProvider{name: "p2", err: ErrNotSupported},
		&fakeProvider{name: "p3", q: want},
	)
	var failed []string
	c.SetOnProviderFail(func(name, method string, err error) {
		failed = append(failed, name)
	})

	got, err := c.GetQuote(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("got %+v", got)
	}
	if len(failed) != 1 || failed[0] != "p1" {
		t.Errorf("OnProviderFail called with %v", failed)
	}
}

func TestClient_AllFail(t *testing.T) {
	c, _ := NewClient(
		&fakeProvider{name: "p1", err: errors.New("e1")},
		&fakeProvider{name: "p2", err: errors.New("e2")},
	)
	_, err := c.GetQuote(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
}

func TestClient_NoProvider(t *testing.T) {
	_, err := NewClient()
	if err == nil {
		t.Fatal("want error for empty providers")
	}
	_, err2 := NewClient()
	if err2 == nil {
		t.Fatal("NewClient() should return error when no providers given")
	}
}

func TestClient_AllNotSupported(t *testing.T) {
	c, _ := NewClient(
		&fakeProvider{name: "p1", err: ErrNotSupported},
		&fakeProvider{name: "p2", err: ErrNotSupported},
	)
	_, err := c.GetStockList(context.Background())
	if err == nil {
		t.Fatal("want error when no provider supports the method")
	}
}

func TestClient_ErrNotSupported_SkippedNotFailed(t *testing.T) {
	want := []*Kline{{Open: 1.0}}
	c, _ := NewClient(
		&fakeProvider{name: "sina", err: ErrNotSupported},
		&fakeProvider{name: "eastmoney", k: want},
	)
	var failed []string
	c.SetOnProviderFail(func(name, method string, err error) {
		failed = append(failed, name)
	})

	got, err := c.GetKline(context.Background(), StockCode{Market: MarketSH, Code: "600000"}, KlinePeriod1Min, 1)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) != 1 || got[0].Open != 1.0 {
		t.Fatalf("got %+v", got)
	}
	if len(failed) != 0 {
		t.Errorf("OnProviderFail should not be called for ErrNotSupported, got: %v", failed)
	}
}

func TestClient_PartialErrorReturnedWithoutFailover(t *testing.T) {
	want := []*Quote{{Name: "partial"}}
	c, _ := NewClient(
		&fakeProvider{name: "p1", q: want, err: &PartialError{Failures: []error{errors.New("bad record")}}},
		&fakeProvider{name: "p2", q: []*Quote{{Name: "fallback"}}},
	)
	var failed []string
	c.SetOnProviderFail(func(name, method string, err error) {
		failed = append(failed, name)
	})

	got, err := c.GetQuote(context.Background())
	var pe *PartialError
	if !errors.As(err, &pe) {
		t.Fatalf("want PartialError, got %v", err)
	}
	if len(got) != 1 || got[0].Name != "partial" {
		t.Fatalf("got %+v", got)
	}
	if len(failed) != 0 {
		t.Fatalf("unexpected provider fail callbacks: %v", failed)
	}
}
