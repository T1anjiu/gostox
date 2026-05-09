package gostox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrNotSupported 表示某个 provider 不支持特定方法，调用方应跳过并尝试下一个 provider。
var ErrNotSupported = errors.New("gostox: this method is not supported by this provider")

// PartialError 表示批量操作中部分条目解析失败。
// 调用方可通过 errors.As 获取失败详情，同时仍能使用已成功解析的数据。
type PartialError struct {
	Failures []error // 每条失败记录对应一个错误
}

func (e *PartialError) Error() string {
	return fmt.Sprintf("gostox: %d record(s) failed to parse", len(e.Failures))
}

// Market 表示证券所在市场。
type Market int

const (
	MarketSH Market = iota // 上海证券交易所
	MarketSZ                // 深圳证券交易所
	MarketBJ                // 北京证券交易所
)

func (m Market) String() string {
	switch m {
	case MarketSH:
		return "sh"
	case MarketSZ:
		return "sz"
	case MarketBJ:
		return "bj"
	default:
		return "unknown"
	}
}

// EastmoneySecID 返回东方财富 secid 前半部分（1=沪，0=深），用作前缀。
func (m Market) EastmoneySecID() string {
	switch m {
	case MarketSH:
		return "1"
	case MarketSZ:
		return "0"
	case MarketBJ:
		return "0" // 北交所在东方财富中 market ID 与深市同为 0
	default:
		return ""
	}
}

// StockCode 唯一标识一只 A 股。
type StockCode struct {
	Market Market
	Code   string
}

func (s StockCode) String() string {
	return fmt.Sprintf("%s%s", s.Market, s.Code)
}

// SinaCode 返回新浪接口使用的代码格式，如 sh600000。
func (s StockCode) SinaCode() string { return s.String() }

// TencentCode 返回腾讯接口使用的代码格式，如 sh600000。
func (s StockCode) TencentCode() string { return s.String() }

// EastmoneyCode 返回东方财富 secid 格式，如 1.600000。
func (s StockCode) EastmoneyCode() string {
	return fmt.Sprintf("%s.%s", s.Market.EastmoneySecID(), s.Code)
}

// ParseStockCode 解析带市场前缀的代码，如 "sh600000"、"sz000001"。
func ParseStockCode(raw string) (StockCode, error) {
	if len(raw) < 3 {
		return StockCode{}, fmt.Errorf("invalid stock code: %q", raw)
	}
	var m Market
	prefix := strings.ToLower(raw[:2])
	code := raw[2:]
	switch prefix {
	case "sh":
		m = MarketSH
	case "sz":
		m = MarketSZ
	case "bj":
		m = MarketBJ
	default:
		return StockCode{}, fmt.Errorf("unknown market prefix: %q", prefix)
	}
	return StockCode{Market: m, Code: code}, nil
}

// InferMarket 按代码首字符推断市场（粗略规则，未覆盖所有板块）。
func InferMarket(code string) StockCode {
	if len(code) == 0 {
		return StockCode{}
	}
	switch code[0] {
	case '6':
		return StockCode{Market: MarketSH, Code: code}
	case '0', '3':
		return StockCode{Market: MarketSZ, Code: code}
	case '8', '4':
		return StockCode{Market: MarketBJ, Code: code}
	default:
		return StockCode{Market: MarketSH, Code: code}
	}
}

// KlinePeriod 表示 K 线周期。
type KlinePeriod int

const (
	KlinePeriod1Min KlinePeriod = iota + 1
	KlinePeriod5Min
	KlinePeriod15Min
	KlinePeriod30Min
	KlinePeriod60Min
	KlinePeriodDay
	KlinePeriodWeek
	KlinePeriodMonth
)

// Quote 表示实时行情快照。
type Quote struct {
	Code      StockCode
	Name      string
	Current   float64
	Open      float64
	PrevClose float64
	Close     float64 // 盘中等同于 Current（最新成交价），收盘后为当日收盘价。实时接口无法区分二者。
	High      float64
	Low       float64
	Volume    int64
	Amount    float64
	Change    float64
	ChangePct float64
	Timestamp time.Time
}

// Kline 表示一根 K 线。
type Kline struct {
	Code      StockCode
	Open      float64
	Close     float64
	High      float64
	Low       float64
	Volume    int64
	Amount    float64
	Timestamp time.Time
	Period    KlinePeriod
}

// StockInfo 表示股票基础信息。
type StockInfo struct {
	Code StockCode
	Name string
}

// Provider 定义一个行情数据提供方。
// 所有方法都带 context，以便调用方传递超时与取消。
type Provider interface {
	Name() string
	GetQuote(ctx context.Context, codes ...StockCode) ([]*Quote, error)
	GetKline(ctx context.Context, code StockCode, period KlinePeriod, count int) ([]*Kline, error)
	GetStockList(ctx context.Context) ([]*StockInfo, error)
}

// Client 在多个 provider 之间做故障转移。
type Client struct {
	providers  []Provider
	mu         sync.RWMutex
	onProviderFail func(providerName, method string, err error)
}

// NewClient 按优先级顺序接收 provider 列表。至少需要传入一个 provider，否则返回错误。
func NewClient(providers ...Provider) (*Client, error) {
	if len(providers) == 0 {
		return nil, errors.New("gostox: at least one provider is required")
	}
	for i, p := range providers {
		if p == nil {
			return nil, fmt.Errorf("gostox: provider[%d] is nil", i)
		}
	}
	return &Client{providers: providers}, nil
}

// SetOnProviderFail 设置 provider 失败时的回调函数，可用于打日志、埋点等。
// 允许传入 nil 以清除回调。并发安全。
func (c *Client) SetOnProviderFail(fn func(providerName, method string, err error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onProviderFail = fn
}

// AddProvider 追加一个 provider 到列表末尾。
func (c *Client) AddProvider(p Provider) {
	if p == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.providers = append(c.providers, p)
}

// Providers 返回当前 provider 列表的快照。
func (c *Client) Providers() []Provider {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Provider, len(c.providers))
	copy(out, c.providers)
	return out
}

// tryProviders 在 provider 列表上执行 fn，直到返回 nil 错误。
// 对返回 ErrNotSupported 的 provider 直接跳过；其它错误记录并继续。
// 全部失败时返回首个非 ErrNotSupported 错误（保留历史信息用 errors.Join）。
func tryProviders[T any](
	c *Client,
	method string,
	fn func(p Provider) (T, error),
) (T, error) {
	c.mu.RLock()
	providers := make([]Provider, len(c.providers))
	copy(providers, c.providers)
	onFail := c.onProviderFail
	c.mu.RUnlock()

	var zero T
	if len(providers) == 0 {
		return zero, errors.New("gostox: no providers configured")
	}

	var errs []error
	for _, p := range providers {
		result, err := fn(p)
		if err == nil {
			return result, nil
		}
		var partialErr *PartialError
		if errors.As(err, &partialErr) {
			return result, err
		}
		if errors.Is(err, ErrNotSupported) {
			continue
		}
		if onFail != nil {
			onFail(p.Name(), method, err)
		}
		errs = append(errs, fmt.Errorf("%s: %w", p.Name(), err))
	}
	if len(errs) == 0 {
		return zero, fmt.Errorf("gostox: no provider supports %s", method)
	}
	return zero, fmt.Errorf("gostox: all providers failed for %s: %w", method, errors.Join(errs...))
}

// GetQuote 按 provider 顺序尝试获取实时行情。
func (c *Client) GetQuote(ctx context.Context, codes ...StockCode) ([]*Quote, error) {
	return tryProviders(c, "GetQuote", func(p Provider) ([]*Quote, error) {
		return p.GetQuote(ctx, codes...)
	})
}

// GetKline 按 provider 顺序尝试获取 K 线。
func (c *Client) GetKline(ctx context.Context, code StockCode, period KlinePeriod, count int) ([]*Kline, error) {
	return tryProviders(c, "GetKline", func(p Provider) ([]*Kline, error) {
		return p.GetKline(ctx, code, period, count)
	})
}

// GetStockList 按 provider 顺序尝试拉取 A 股列表。
func (c *Client) GetStockList(ctx context.Context) ([]*StockInfo, error) {
	return tryProviders(c, "GetStockList", func(p Provider) ([]*StockInfo, error) {
		return p.GetStockList(ctx)
	})
}
