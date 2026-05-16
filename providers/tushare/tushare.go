// Package tushare 封装 Tushare Pro 行情接口。
package tushare

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	gostox "github.com/T1anjiu/gostox"
	"golang.org/x/time/rate"
)

const (
	quoteURL      = "https://api.tushare.pro/stock/quotations"
	klineURL      = "https://api.tushare.pro/stock/day"
	stockListURL  = "https://api.tushare.pro/stock/basic"
	indexQuoteURL = "https://api.tushare.pro/index/quotations"
	indexKlineURL = "https://api.tushare.pro/index/day"
	defaultToken  = ""
	userAgent     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"
	maxBodySize   = 4 << 20 // 4MB
)

// Provider 是 Tushare Pro 数据源。
type Provider struct {
	client    *http.Client
	token     string
	limiter   *rate.Limiter
	maxRetries int
	retryBase time.Duration
}

// Option 配置 Provider。
type Option func(*Provider)

// WithHTTPClient 自定义底层 HTTP client。
func WithHTTPClient(c *http.Client) Option {
	return func(p *Provider) { p.client = c }
}

// WithToken 设置 Tushare API token。
func WithToken(token string) Option {
	return func(p *Provider) { p.token = token }
}

// WithRateLimit 设置每秒最大请求数（QPS）。默认 10。
func WithRateLimit(rps float64) Option {
	return func(p *Provider) {
		p.limiter = rate.NewLimiter(rate.Limit(rps), 1)
	}
}

// WithRetry 设置最大重试次数和初始等待时长。默认重试 2 次，初始 200ms。
func WithRetry(maxRetries int, baseDelay time.Duration) Option {
	return func(p *Provider) {
		p.maxRetries = maxRetries
		p.retryBase = baseDelay
	}
}

// NewProvider 创建 Tushare Provider。
func NewProvider(opts ...Option) *Provider {
	p := &Provider{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		token:      defaultToken,
		limiter:    rate.NewLimiter(10, 1),
		maxRetries: 2,
		retryBase:  200 * time.Millisecond,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name 返回 provider 名称。
func (p *Provider) Name() string { return "tushare" }

// doRequest 限流 + 重试的 HTTP POST 请求。
func (p *Provider) doRequest(ctx context.Context, rawURL string, params url.Values) ([]byte, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("tushare rate limit: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= p.maxRetries; attempt++ {
		if attempt > 0 {
			wait := p.retryBase * (1 << (attempt - 1))
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		body, err := p.doRequestOnce(ctx, rawURL, params)
		if err == nil {
			return body, nil
		}
		if !isRetryable(err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if err == context.Canceled || err == context.DeadlineExceeded {
		return false
	}
	var httpErr *httpStatusError
	_ = httpErr
	return true
}

type httpStatusError struct {
	StatusCode int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("http status %d", e.StatusCode)
}

func (p *Provider) doRequestOnce(ctx context.Context, rawURL string, params url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &httpStatusError{StatusCode: resp.StatusCode}
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
}

// Tushare API 通用响应格式
type tushareResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// GetQuote 查询多只股票的实时行情。
func (p *Provider) GetQuote(ctx context.Context, codes ...gostox.StockCode) ([]*gostox.Quote, error) {
	if len(codes) == 0 {
		return nil, nil
	}

	requested := make(map[string]gostox.StockCode, len(codes))
	for _, c := range codes {
		requested[c.String()] = c
	}

	params := url.Values{}
	params.Set("token", p.token)
	params.Set("exchange", "CNY")
	params.Set("fields", "trade_date,close,open,high,low,pre_close,volume,amount,turnover,change,pct_change")
	params.Set("date", "20240102")

	body, err := p.doRequest(ctx, quoteURL, params)
	if err != nil {
		return nil, fmt.Errorf("tushare quote: %w", err)
	}

	var resp tushareResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("tushare quote unmarshal: %w", err)
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("tushare quote api code=%d msg=%s", resp.Code, resp.Msg)
	}

	var result []*gostox.Quote
	var parseErrs []error

	var items []map[string]interface{}
	if err := json.Unmarshal(resp.Data, &items); err != nil {
		return nil, fmt.Errorf("tushare quote parse data: %w", err)
	}

	for _, item := range items {
		code, err := parseTSCode(item)
		if err != nil {
			parseErrs = append(parseErrs, fmt.Errorf("parse code: %w", err))
			continue
		}

		q, err := parseTushareQuote(item)
		if err != nil {
			parseErrs = append(parseErrs, fmt.Errorf("parse quote %s: %w", code, err))
			continue
		}
		q.Code = code
		result = append(result, q)
		delete(requested, code.String())
	}

	for _, c := range requested {
		parseErrs = append(parseErrs, fmt.Errorf("missing quote for %s", c))
	}

	if len(parseErrs) > 0 {
		return result, &gostox.PartialError{Failures: parseErrs}
	}
	return result, nil
}

// GetKline 查询 K 线。
func (p *Provider) GetKline(ctx context.Context, code gostox.StockCode, period gostox.KlinePeriod, count int) ([]*gostox.Kline, error) {
	periodStr, err := toTusharePeriod(period)
	if err != nil {
		return nil, fmt.Errorf("tushare kline: %w", err)
	}

	params := url.Values{}
	params.Set("token", p.token)
	params.Set("secid", code.String())
	params.Set("fields", "date,open,high,low,close,volume,amount,turnover")
	params.Set("start_date", "20200101")
	params.Set("end_date", "20500101")
	params.Set("count", strconv.Itoa(count))
	params.Set("period", periodStr)

	body, err := p.doRequest(ctx, klineURL, params)
	if err != nil {
		return nil, fmt.Errorf("tushare kline: %w", err)
	}

	var resp tushareResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("tushare kline unmarshal: %w", err)
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("tushare kline api code=%d msg=%s", resp.Code, resp.Msg)
	}

	var items []map[string]interface{}
	if err := json.Unmarshal(resp.Data, &items); err != nil {
		return nil, fmt.Errorf("tushare kline parse data: %w", err)
	}

	klines := make([]*gostox.Kline, 0, len(items))
	var parseErrs []error
	for _, item := range items {
		k, err := parseTushareKline(item, code, period)
		if err != nil {
			parseErrs = append(parseErrs, err)
			continue
		}
		klines = append(klines, k)
	}

	if len(parseErrs) > 0 {
		return klines, &gostox.PartialError{Failures: parseErrs}
	}
	return klines, nil
}

// GetStockList 拉取沪深 A 股列表。
func (p *Provider) GetStockList(ctx context.Context) ([]*gostox.StockInfo, error) {
	params := url.Values{}
	params.Set("token", p.token)
	params.Set("exchange", "CNY")
	params.Set("list_status", "L")
	params.Set("fields", "symbol,name,area,industry,market,check_date,ipo_date,region")
	params.Set("page_size", "5000")

	body, err := p.doRequest(ctx, stockListURL, params)
	if err != nil {
		return nil, fmt.Errorf("tushare stocklist: %w", err)
	}

	var resp tushareResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("tushare stocklist unmarshal: %w", err)
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("tushare stocklist api code=%d msg=%s", resp.Code, resp.Msg)
	}

	var items []map[string]interface{}
	if err := json.Unmarshal(resp.Data, &items); err != nil {
		return nil, fmt.Errorf("tushare stocklist parse data: %w", err)
	}

	result := make([]*gostox.StockInfo, 0, len(items))
	var parseErrs []error
	for _, item := range items {
		symbol, ok := item["symbol"].(string)
		if !ok || symbol == "" {
			parseErrs = append(parseErrs, fmt.Errorf("missing symbol"))
			continue
		}
		name, _ := item["name"].(string)

		// 解析市场
		market := gostox.MarketSH
		if len(symbol) >= 2 {
			prefix := strings.ToLower(symbol[:2])
			if prefix == "sz" || prefix == "bj" {
				market = gostox.MarketSZ
			}
		}

		code := gostox.StockCode{Market: market, Code: symbol}
		result = append(result, &gostox.StockInfo{Code: code, Name: name})
	}

	if len(parseErrs) > 0 {
		return result, &gostox.PartialError{Failures: parseErrs}
	}
	return result, nil
}

// GetIndexQuote 查询指数实时行情。
func (p *Provider) GetIndexQuote(ctx context.Context, codes ...gostox.IndexCode) ([]*gostox.IndexQuote, error) {
	if len(codes) == 0 {
		return nil, nil
	}

	requested := make(map[string]gostox.IndexCode, len(codes))
	for _, c := range codes {
		requested[c.String()] = c
	}

	params := url.Values{}
	params.Set("token", p.token)
	params.Set("exchange", "CNY")
	params.Set("fields", "trade_date,close,open,high,low,pre_close,volume,amount,turnover,change,pct_change")
	params.Set("date", "20240102")

	body, err := p.doRequest(ctx, indexQuoteURL, params)
	if err != nil {
		return nil, fmt.Errorf("tushare index quote: %w", err)
	}

	var resp tushareResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("tushare index quote unmarshal: %w", err)
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("tushare index quote api code=%d msg=%s", resp.Code, resp.Msg)
	}

	var items []map[string]interface{}
	if err := json.Unmarshal(resp.Data, &items); err != nil {
		return nil, fmt.Errorf("tushare index quote parse data: %w", err)
	}

	result := make([]*gostox.IndexQuote, 0, len(items))
	var parseErrs []error
	for _, item := range items {
		symbol, ok := item["symbol"].(string)
		if !ok {
			parseErrs = append(parseErrs, fmt.Errorf("missing symbol in index quote"))
			continue
		}
		idxCode := gostox.IndexCode{Code: symbol}
		q, err := parseTushareIndexQuote(item)
		if err != nil {
			parseErrs = append(parseErrs, fmt.Errorf("parse index quote %s: %w", symbol, err))
			continue
		}
		q.Code = idxCode
		result = append(result, q)
		delete(requested, symbol)
	}

	for _, c := range requested {
		parseErrs = append(parseErrs, fmt.Errorf("missing index quote for %s", c))
	}

	if len(parseErrs) > 0 {
		return result, &gostox.PartialError{Failures: parseErrs}
	}
	return result, nil
}

// GetIndexKline 查询指数 K 线。
func (p *Provider) GetIndexKline(ctx context.Context, code gostox.IndexCode, period gostox.KlinePeriod, count int) ([]*gostox.IndexKline, error) {
	periodStr, err := toTusharePeriod(period)
	if err != nil {
		return nil, fmt.Errorf("tushare index kline: %w", err)
	}

	params := url.Values{}
	params.Set("token", p.token)
	params.Set("secid", code.String())
	params.Set("fields", "date,open,high,low,close,volume,amount,turnover")
	params.Set("start_date", "20200101")
	params.Set("end_date", "20500101")
	params.Set("count", strconv.Itoa(count))
	params.Set("period", periodStr)

	body, err := p.doRequest(ctx, indexKlineURL, params)
	if err != nil {
		return nil, fmt.Errorf("tushare index kline: %w", err)
	}

	var resp tushareResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("tushare index kline unmarshal: %w", err)
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("tushare index kline api code=%d msg=%s", resp.Code, resp.Msg)
	}

	var items []map[string]interface{}
	if err := json.Unmarshal(resp.Data, &items); err != nil {
		return nil, fmt.Errorf("tushare index kline parse data: %w", err)
	}

	klines := make([]*gostox.IndexKline, 0, len(items))
	var parseErrs []error
	for _, item := range items {
		k, err := parseTushareIndexKline(item, code, period)
		if err != nil {
			parseErrs = append(parseErrs, err)
			continue
		}
		klines = append(klines, k)
	}

	if len(parseErrs) > 0 {
		return klines, &gostox.PartialError{Failures: parseErrs}
	}
	return klines, nil
}

func toTusharePeriod(p gostox.KlinePeriod) (string, error) {
	switch p {
	case gostox.KlinePeriod1Min:
		return "1", nil
	case gostox.KlinePeriod5Min:
		return "5", nil
	case gostox.KlinePeriod15Min:
		return "15", nil
	case gostox.KlinePeriod30Min:
		return "30", nil
	case gostox.KlinePeriod60Min:
		return "60", nil
	case gostox.KlinePeriodDay:
		return "D", nil
	case gostox.KlinePeriodWeek:
		return "W", nil
	case gostox.KlinePeriodMonth:
		return "M", nil
	default:
		return "", fmt.Errorf("tushare: unsupported kline period %d", p)
	}
}

func parseTSCode(item map[string]interface{}) (gostox.StockCode, error) {
	symbol, ok := item["symbol"].(string)
	if !ok || symbol == "" {
		return gostox.StockCode{}, fmt.Errorf("missing symbol")
	}
	return gostox.ParseStockCode(symbol)
}

func parseTushareQuote(item map[string]interface{}) (*gostox.Quote, error) {
	close, _ := item["close"].(float64)
	open, _ := item["open"].(float64)
	high, _ := item["high"].(float64)
	low, _ := item["low"].(float64)
	preClose, _ := item["pre_close"].(float64)
	volume, _ := item["volume"].(float64)
	amount, _ := item["amount"].(float64)
	_, _ = item["turnover"].(float64)
	change, _ := item["change"].(float64)
	pctChange, _ := item["pct_change"].(float64)

	tradeDate, _ := item["trade_date"].(string)
	timestamp := time.Now()
	if tradeDate != "" {
		ts, err := time.Parse("2006-01-02", tradeDate)
		if err == nil {
			timestamp = ts
		}
	}

	return &gostox.Quote{
		Name:      "",
		Current:   close,
		Open:      open,
		PrevClose: preClose,
		Close:     close,
		High:      high,
		Low:       low,
		Volume:    int64(volume),
		Amount:    amount,
		Change:    change,
		ChangePct: pctChange,
		Timestamp: timestamp,
	}, nil
}

func parseTushareKline(item map[string]interface{}, code gostox.StockCode, period gostox.KlinePeriod) (*gostox.Kline, error) {
	open, _ := item["open"].(float64)
	high, _ := item["high"].(float64)
	low, _ := item["low"].(float64)
	close, _ := item["close"].(float64)
	volume, _ := item["volume"].(float64)
	amount, _ := item["amount"].(float64)

	date, _ := item["date"].(string)
	timestamp := time.Now()
	if date != "" {
		ts, err := time.Parse("2006-01-02", date)
		if err == nil {
			timestamp = ts
		}
	}

	return &gostox.Kline{
		Code:      code,
		Open:      open,
		Close:     close,
		High:      high,
		Low:       low,
		Volume:    int64(volume),
		Amount:    amount,
		Timestamp: timestamp,
		Period:    period,
	}, nil
}

func parseTushareIndexQuote(item map[string]interface{}) (*gostox.IndexQuote, error) {
	close, _ := item["close"].(float64)
	open, _ := item["open"].(float64)
	high, _ := item["high"].(float64)
	low, _ := item["low"].(float64)
	preClose, _ := item["pre_close"].(float64)
	volume, _ := item["volume"].(float64)
	amount, _ := item["amount"].(float64)
	change, _ := item["change"].(float64)
	pctChange, _ := item["pct_change"].(float64)

	tradeDate, _ := item["trade_date"].(string)
	timestamp := time.Now()
	if tradeDate != "" {
		ts, err := time.Parse("2006-01-02", tradeDate)
		if err == nil {
			timestamp = ts
		}
	}

	return &gostox.IndexQuote{
		Name:      "",
		Current:   close,
		Open:      open,
		PrevClose: preClose,
		Close:     close,
		High:      high,
		Low:       low,
		Volume:    int64(volume),
		Amount:    amount,
		Change:    change,
		ChangePct: pctChange,
		Timestamp: timestamp,
	}, nil
}

func parseTushareIndexKline(item map[string]interface{}, code gostox.IndexCode, period gostox.KlinePeriod) (*gostox.IndexKline, error) {
	open, _ := item["open"].(float64)
	high, _ := item["high"].(float64)
	low, _ := item["low"].(float64)
	close, _ := item["close"].(float64)
	volume, _ := item["volume"].(float64)
	amount, _ := item["amount"].(float64)

	date, _ := item["date"].(string)
	timestamp := time.Now()
	if date != "" {
		ts, err := time.Parse("2006-01-02", date)
		if err == nil {
			timestamp = ts
		}
	}

	return &gostox.IndexKline{
		Code:      code,
		Open:      open,
		Close:     close,
		High:      high,
		Low:       low,
		Volume:    int64(volume),
		Amount:    amount,
		Timestamp: timestamp,
		Period:    period,
	}, nil
}

var _ gostox.Provider = (*Provider)(nil)
