// Package eastmoney 封装东方财富行情接口。
package eastmoney

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	gostox "github.com/T1anjiu/gostox"
	utls "github.com/refraction-networking/utls"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

const (
	quoteURL        = "https://push2.eastmoney.com/api/qt/ulist.np/get"
	klineURL        = "https://push2his.eastmoney.com/api/qt/stock/kline/get"
	stockListURL    = "https://push2.eastmoney.com/api/qt/clist/get"
	defaultUtToken  = "fa5fd1943c7b386f172d6893dbfba10b"
	maxBodySize     = 4 << 20 // 4MB，单次响应上限
	quoteChunkSize  = 100     // 每批最多请求 100 只，避免 URL 过长
)

// 东方财富接口返回字段定义：
//   f2  最新价           f3  涨跌幅(%)      f4  涨跌额
//   f5  成交量(手)       f6  成交额(元)     f7  振幅(%)
//   f8  换手率(%)        f9  市盈率(动态)   f10 量比
//   f12 股票代码         f13 市场(1=沪,0=深) f14 股票名称
//   f15 最高价           f16 最低价         f17 开盘价
//   f18 昨收
//
// 当请求参数使用 fltt=2 时，返回值已为真实价格（浮点），无需再除以 100。
// 若使用 fltt=1，则返回整数需除以 100。旧版本误用 flts 导致价格被额外缩放。
const quoteFields = "f2,f3,f4,f5,f6,f7,f8,f9,f10,f12,f13,f14,f15,f16,f17,f18"

// K 线字段：f51=日期 f52=开 f53=收 f54=高 f55=低 f56=成交量 f57=成交额
const klineFields2 = "f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61"

// 指数行情 URL 复用 quoteURL，K 线复用 klineURL

// Provider 是东方财富数据源。
type Provider struct {
	client      *http.Client
	utToken     string
	limiter     *rate.Limiter
	maxRetries  int
	retryBase   time.Duration
	concurrency int
}

// Option 配置 Provider。
type Option func(*Provider)

// WithHTTPClient 自定义底层 HTTP client。
func WithHTTPClient(c *http.Client) Option {
	return func(p *Provider) { p.client = c }
}

// WithUTToken 设置东方财富接口的 ut 认证 token。
// 默认使用内置 token，token 失效时通过此选项注入新值，无需重新编译。
func WithUTToken(token string) Option {
	return func(p *Provider) { p.utToken = token }
}

// WithRateLimit 设置每秒最大请求数（QPS）。默认 5。
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

// WithConcurrency 设置并发分块请求的最大并发数。默认 5。
func WithConcurrency(n int) Option {
	return func(p *Provider) {
		p.concurrency = n
	}
}

// NewProvider 创建东方财富 Provider。
func NewProvider(opts ...Option) *Provider {
	p := &Provider{
		client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: newBrowserTransport(),
		},
		utToken:     defaultUtToken,
		limiter:     rate.NewLimiter(5, 1),
		maxRetries:  2,
		retryBase:   200 * time.Millisecond,
		concurrency: 5,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name 返回 provider 名称。
func (p *Provider) Name() string { return "eastmoney" }

// GetQuote 查询多只股票的实时行情。
// 超过 quoteChunkSize 的请求会自动分批并发发送。
func (p *Provider) GetQuote(ctx context.Context, codes ...gostox.StockCode) ([]*gostox.Quote, error) {
	if len(codes) == 0 {
		return nil, nil
	}

	requested := make(map[string]gostox.StockCode, len(codes))
	secIDs := make([]string, 0, len(codes))
	for _, c := range codes {
		requested[c.String()] = c
		secIDs = append(secIDs, c.EastmoneyCode())
	}

	// 分批
	type chunk struct {
		secIDs  []string
		codes   map[string]gostox.StockCode
		now     time.Time
		index   int
	}
	var chunks []chunk
	for i := 0; i < len(secIDs); i += quoteChunkSize {
		end := i + quoteChunkSize
		if end > len(secIDs) {
			end = len(secIDs)
		}
		chunks = append(chunks, chunk{
			secIDs: secIDs[i:end],
			codes:  requested,
			now:    time.Now(),
			index:  len(chunks),
		})
	}

	type chunkResult struct {
		quotes    []*gostox.Quote
		parseErrs []error
	}
	results := make([]chunkResult, len(chunks))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(p.concurrency)

	for i := range chunks {
		i := i
		ch := chunks[i]
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}

			params := url.Values{}
			params.Set("fltt", "2")
			params.Set("secids", strings.Join(ch.secIDs, ","))
			params.Set("fields", quoteFields)
			params.Set("ut", p.utToken)

			body, err := p.doGet(gctx, quoteURL, params)
			if err != nil {
				return fmt.Errorf("eastmoney quote: %w", err)
			}

			var resp quoteResponse
			if err := json.Unmarshal(body, &resp); err != nil {
				return fmt.Errorf("eastmoney quote unmarshal: %w", err)
			}
			if resp.Rc != 0 {
				return fmt.Errorf("eastmoney quote api rc=%d", resp.Rc)
			}

			var quotes []*gostox.Quote
			var parseErrs []error
			for _, d := range resp.Data.Diff {
				prefix, err := marketPrefix(d.Market, d.Code)
				if err != nil {
					parseErrs = append(parseErrs, fmt.Errorf("parse market for %q: %w", d.Code, err))
					continue
				}
				code, err := gostox.ParseStockCode(prefix + d.Code)
				if err != nil {
					parseErrs = append(parseErrs, fmt.Errorf("parse code %q: %w", d.Code, err))
					continue
				}
				quotes = append(quotes, &gostox.Quote{
					Code:      code,
					Name:      d.Name,
					Current:   d.Price,
					Open:      d.Open,
					PrevClose: d.PrevClose,
					Close:     d.Price,
					High:      d.High,
					Low:       d.Low,
					Volume:    d.Volume * 100,
					Amount:    d.Amount,
					Change:    d.Change,
					ChangePct: d.ChangePct,
					Timestamp: ch.now,
				})
			}
			results[i] = chunkResult{quotes: quotes, parseErrs: parseErrs}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	var quotes []*gostox.Quote
	var allParseErrs []error
	found := make(map[string]bool, len(secIDs))
	for _, r := range results {
		for _, q := range r.quotes {
			found[q.Code.String()] = true
		}
		quotes = append(quotes, r.quotes...)
		allParseErrs = append(allParseErrs, r.parseErrs...)
	}
	for _, c := range codes {
		if !found[c.String()] {
			allParseErrs = append(allParseErrs, fmt.Errorf("missing quote for %s", c))
		}
	}
	if len(allParseErrs) > 0 {
		return quotes, &gostox.PartialError{Failures: allParseErrs}
	}
	return quotes, nil
}

// GetKline 查询 K 线。
func (p *Provider) GetKline(ctx context.Context, code gostox.StockCode, period gostox.KlinePeriod, count int) ([]*gostox.Kline, error) {
	klt, err := toKlineType(period)
	if err != nil {
		return nil, fmt.Errorf("eastmoney kline: %w", err)
	}

	params := url.Values{}
	params.Set("secid", code.EastmoneyCode())
	params.Set("fields1", "f1,f2,f3,f4,f5,f6")
	params.Set("fields2", klineFields2)
	params.Set("klt", strconv.Itoa(klt))
	params.Set("fqt", "1") // 前复权
	params.Set("end", "20500101")
	params.Set("lmt", strconv.Itoa(count))
	params.Set("ut", p.utToken)

	body, err := p.doGet(ctx, klineURL, params)
	if err != nil {
		return nil, fmt.Errorf("eastmoney kline: %w", err)
	}

	var resp klineResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("eastmoney kline unmarshal: %w", err)
	}
	if resp.Rc != 0 {
		return nil, fmt.Errorf("eastmoney kline api rc=%d", resp.Rc)
	}

	klines := make([]*gostox.Kline, 0, len(resp.Data.Klines))
	var parseErrs []error
	for _, line := range resp.Data.Klines {
		k, err := parseKlineLine(line, code, period)
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

// GetStockList 拉取沪深 A 股列表（分页）。
// fs 过滤：m:0+t:6 深主板, m:0+t:80 创业板, m:1+t:2 沪主板, m:1+t:23 科创板。
// 最多拉取 maxStockListPages 页，防止接口异常时无限循环。
func (p *Provider) GetStockList(ctx context.Context) ([]*gostox.StockInfo, error) {
	var all []*gostox.StockInfo
	const (
		pageSize         = 100
		maxStockListPages = 60 // 东方财富限制每页最多 100 条，A 股约 5500 只，需 55 页
	)
	var allParseErrs []error
	for pn := 1; pn <= maxStockListPages; pn++ {
		if err := ctx.Err(); err != nil {
			return all, fmt.Errorf("eastmoney stocklist: %w", err)
		}
		params := url.Values{}
		params.Set("pn", strconv.Itoa(pn))
		params.Set("pz", strconv.Itoa(pageSize))
		params.Set("po", "1")
		params.Set("np", "1")
		params.Set("fltt", "2")
		params.Set("invt", "2")
		params.Set("fid", "f3")
		params.Set("fs", "m:0+t:6,m:0+t:80,m:1+t:2,m:1+t:23")
		params.Set("fields", "f12,f13,f14")
		params.Set("ut", p.utToken)

		body, err := p.doGet(ctx, stockListURL, params)
		if err != nil {
			return all, fmt.Errorf("eastmoney stocklist page %d: %w", pn, err)
		}

		var resp stockListResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return all, fmt.Errorf("eastmoney stocklist unmarshal: %w", err)
		}
		if resp.Rc != 0 {
			return all, fmt.Errorf("eastmoney stocklist api rc=%d", resp.Rc)
		}

		for _, d := range resp.Data.Diff {
			prefix, err := marketPrefix(d.Market, d.Code)
			if err != nil {
				allParseErrs = append(allParseErrs, fmt.Errorf("page %d parse market for %q: %w", pn, d.Code, err))
				continue
			}
			code, err := gostox.ParseStockCode(prefix + d.Code)
			if err != nil {
				allParseErrs = append(allParseErrs, fmt.Errorf("page %d parse code %q: %w", pn, d.Code, err))
				continue
			}
			all = append(all, &gostox.StockInfo{Code: code, Name: d.Name})
		}

		if len(resp.Data.Diff) < pageSize {
			break
		}
	}
	if len(allParseErrs) > 0 {
		return all, &gostox.PartialError{Failures: allParseErrs}
	}
	return all, nil
}

// GetIndexQuote 查询指数实时行情。
func (p *Provider) GetIndexQuote(ctx context.Context, codes ...gostox.IndexCode) ([]*gostox.IndexQuote, error) {
	if len(codes) == 0 {
		return nil, nil
	}

	requested := make(map[string]gostox.IndexCode, len(codes))
	secIDs := make([]string, 0, len(codes))
	for _, c := range codes {
		requested[c.String()] = c
		secIDs = append(secIDs, c.EastmoneyIndexCode())
	}

	type chunk struct {
		secIDs []string
		now    time.Time
		index  int
	}
	var chunks []chunk
	for i := 0; i < len(secIDs); i += quoteChunkSize {
		end := i + quoteChunkSize
		if end > len(secIDs) {
			end = len(secIDs)
		}
		chunks = append(chunks, chunk{secIDs: secIDs[i:end], now: time.Now(), index: len(chunks)})
	}

	type chunkResult struct {
		quotes    []*gostox.IndexQuote
		parseErrs []error
	}
	results := make([]chunkResult, len(chunks))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(p.concurrency)

	for i := range chunks {
		i := i
		ch := chunks[i]
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}

			params := url.Values{}
			params.Set("fltt", "2")
			params.Set("secids", strings.Join(ch.secIDs, ","))
			params.Set("fields", quoteFields)
			params.Set("ut", p.utToken)

			body, err := p.doGet(gctx, quoteURL, params)
			if err != nil {
				return fmt.Errorf("eastmoney index quote: %w", err)
			}

			var resp quoteResponse
			if err := json.Unmarshal(body, &resp); err != nil {
				return fmt.Errorf("eastmoney index quote unmarshal: %w", err)
			}
			if resp.Rc != 0 {
				return fmt.Errorf("eastmoney index quote api rc=%d", resp.Rc)
			}

			var quotes []*gostox.IndexQuote
			var parseErrs []error
			for _, d := range resp.Data.Diff {
				idxCode := gostox.IndexCode{Code: d.Code}
				quotes = append(quotes, &gostox.IndexQuote{
					Code:      idxCode,
					Name:      d.Name,
					Current:   d.Price,
					Open:      d.Open,
					PrevClose: d.PrevClose,
					Close:     d.Price,
					High:      d.High,
					Low:       d.Low,
					Volume:    d.Volume * 100,
					Amount:    d.Amount,
					Change:    d.Change,
					ChangePct: d.ChangePct,
					Timestamp: ch.now,
				})
			}
			results[i] = chunkResult{quotes: quotes, parseErrs: parseErrs}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	var quotes []*gostox.IndexQuote
	var allParseErrs []error
	found := make(map[string]bool, len(secIDs))
	for _, r := range results {
		for _, q := range r.quotes {
			found[q.Code.String()] = true
		}
		quotes = append(quotes, r.quotes...)
		allParseErrs = append(allParseErrs, r.parseErrs...)
	}
	for _, c := range codes {
		if !found[c.String()] {
			allParseErrs = append(allParseErrs, fmt.Errorf("missing index quote for %s", c))
		}
	}
	if len(allParseErrs) > 0 {
		return quotes, &gostox.PartialError{Failures: allParseErrs}
	}
	return quotes, nil
}

// GetIndexKline 查询指数 K 线。
func (p *Provider) GetIndexKline(ctx context.Context, code gostox.IndexCode, period gostox.KlinePeriod, count int) ([]*gostox.IndexKline, error) {
	klt, err := toKlineType(period)
	if err != nil {
		return nil, fmt.Errorf("eastmoney index kline: %w", err)
	}

	params := url.Values{}
	params.Set("secid", code.EastmoneyIndexCode())
	params.Set("fields1", "f1,f2,f3,f4,f5,f6")
	params.Set("fields2", klineFields2)
	params.Set("klt", strconv.Itoa(klt))
	params.Set("fqt", "1")
	params.Set("end", "20500101")
	params.Set("lmt", strconv.Itoa(count))
	params.Set("ut", p.utToken)

	body, err := p.doGet(ctx, klineURL, params)
	if err != nil {
		return nil, fmt.Errorf("eastmoney index kline: %w", err)
	}

	var resp klineResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("eastmoney index kline unmarshal: %w", err)
	}
	if resp.Rc != 0 {
		return nil, fmt.Errorf("eastmoney index kline api rc=%d", resp.Rc)
	}

	klines := make([]*gostox.IndexKline, 0, len(resp.Data.Klines))
	var parseErrs []error
	for _, line := range resp.Data.Klines {
		k, err := parseIndexKlineLine(line, code, period)
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

func (p *Provider) doGet(ctx context.Context, rawURL string, params url.Values) ([]byte, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("eastmoney rate limit: %w", err)
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

		body, err := p.doGetOnce(ctx, rawURL, params)
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

type httpStatusError struct {
	StatusCode int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("http status %d", e.StatusCode)
}

func isRetryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var httpErr *httpStatusError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode >= 500
	}
	return true
}

func (p *Provider) doGetOnce(ctx context.Context, rawURL string, params url.Values) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	u.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

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

func marketPrefix(market int, code string) (string, error) {
	if market == 1 {
		return "sh", nil
	}
	if market == 0 {
		if len(code) > 0 && (code[0] == '8' || code[0] == '4') {
			return "bj", nil
		}
		return "sz", nil
	}
	return "", fmt.Errorf("unknown market: %d", market)
}

// 东方财富 klt 值：分钟周期直接用分钟数，日=101，周=102，月=103。
func toKlineType(p gostox.KlinePeriod) (int, error) {
	switch p {
	case gostox.KlinePeriod1Min:
		return 1, nil
	case gostox.KlinePeriod5Min:
		return 5, nil
	case gostox.KlinePeriod15Min:
		return 15, nil
	case gostox.KlinePeriod30Min:
		return 30, nil
	case gostox.KlinePeriod60Min:
		return 60, nil
	case gostox.KlinePeriodDay:
		return 101, nil
	case gostox.KlinePeriodWeek:
		return 102, nil
	case gostox.KlinePeriodMonth:
		return 103, nil
	default:
		return 0, fmt.Errorf("unsupported kline period: %d", p)
	}
}

// K 线字段下标（来自 fields2 顺序）
const (
	klIdxTime   = 0 // 时间
	klIdxOpen   = 1 // 开盘价
	klIdxClose  = 2 // 收盘价
	klIdxHigh   = 3 // 最高价
	klIdxLow    = 4 // 最低价
	klIdxVolume = 5 // 成交量（手）
	klIdxAmount = 6 // 成交额（元）
	klineMinLen = 7
)

func parseKlineLine(line string, code gostox.StockCode, period gostox.KlinePeriod) (*gostox.Kline, error) {
	parts := strings.Split(line, ",")
	if len(parts) < klineMinLen {
		return nil, fmt.Errorf("invalid kline line: %q", line)
	}

	ts, err := parseKlineTimestamp(parts[klIdxTime])
	if err != nil {
		return nil, err
	}

	open, err := parseEastmoneyFloat(parts[klIdxOpen], "open")
	if err != nil {
		return nil, err
	}
	close_, err := parseEastmoneyFloat(parts[klIdxClose], "close")
	if err != nil {
		return nil, err
	}
	high, err := parseEastmoneyFloat(parts[klIdxHigh], "high")
	if err != nil {
		return nil, err
	}
	low, err := parseEastmoneyFloat(parts[klIdxLow], "low")
	if err != nil {
		return nil, err
	}
	vol, err := parseEastmoneyInt(parts[klIdxVolume], "volume")
	if err != nil {
		return nil, err
	}
	amt, err := parseEastmoneyFloat(parts[klIdxAmount], "amount")
	if err != nil {
		return nil, err
	}

	return &gostox.Kline{
		Code:      code,
		Open:      open,
		Close:     close_,
		High:      high,
		Low:       low,
		Volume:    vol * 100,
		Amount:    amt,
		Timestamp: ts,
		Period:    period,
	}, nil
}

func parseIndexKlineLine(line string, code gostox.IndexCode, period gostox.KlinePeriod) (*gostox.IndexKline, error) {
	parts := strings.Split(line, ",")
	if len(parts) < klineMinLen {
		return nil, fmt.Errorf("invalid index kline line: %q", line)
	}

	ts, err := parseKlineTimestamp(parts[klIdxTime])
	if err != nil {
		return nil, err
	}

	open, err := parseEastmoneyFloat(parts[klIdxOpen], "open")
	if err != nil {
		return nil, err
	}
	close_, err := parseEastmoneyFloat(parts[klIdxClose], "close")
	if err != nil {
		return nil, err
	}
	high, err := parseEastmoneyFloat(parts[klIdxHigh], "high")
	if err != nil {
		return nil, err
	}
	low, err := parseEastmoneyFloat(parts[klIdxLow], "low")
	if err != nil {
		return nil, err
	}
	vol, err := parseEastmoneyInt(parts[klIdxVolume], "volume")
	if err != nil {
		return nil, err
	}
	amt, err := parseEastmoneyFloat(parts[klIdxAmount], "amount")
	if err != nil {
		return nil, err
	}

	return &gostox.IndexKline{
		Code:      code,
		Open:      open,
		Close:     close_,
		High:      high,
		Low:       low,
		Volume:    vol * 100,
		Amount:    amt,
		Timestamp: ts,
		Period:    period,
	}, nil
}

func parseKlineTimestamp(raw string) (time.Time, error) {
	ts, err := time.Parse("2006-01-02", raw)
	if err != nil {
		ts, err = time.Parse("2006-01-02 15:04", raw)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse kline time %q: %w", raw, err)
		}
	}
	return ts, nil
}

func parseEastmoneyFloat(raw, field string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, fmt.Errorf("eastmoney kline: parse %s %q: %w", field, raw, err)
	}
	return v, nil
}

func parseEastmoneyInt(raw, field string) (int64, error) {
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("eastmoney kline: parse %s %q: %w", field, raw, err)
	}
	return v, nil
}

type quoteResponse struct {
	Rc   int `json:"rc"`
	Data struct {
		Total int `json:"total"`
		Diff  []struct {
			Price     float64 `json:"f2"`
			ChangePct float64 `json:"f3"`
			Change    float64 `json:"f4"`
			Volume    int64   `json:"f5"`
			Amount    float64 `json:"f6"`
			Amplitude float64 `json:"f7"`
			Turnover  float64 `json:"f8"`
			PE        float64 `json:"f9"`
			VolRatio  float64 `json:"f10"`
			Code      string  `json:"f12"`
			Market    int     `json:"f13"`
			Name      string  `json:"f14"`
			High      float64 `json:"f15"`
			Low       float64 `json:"f16"`
			Open      float64 `json:"f17"`
			PrevClose float64 `json:"f18"`
		} `json:"diff"`
	} `json:"data"`
}

type klineResponse struct {
	Rc   int `json:"rc"`
	Data struct {
		Code   string   `json:"code"`
		Market int      `json:"market"`
		Name   string   `json:"name"`
		Klines []string `json:"klines"`
	} `json:"data"`
}

type stockListResponse struct {
	Rc   int `json:"rc"`
	Data struct {
		Total int `json:"total"`
		Diff  []struct {
			Code   string `json:"f12"`
			Name   string `json:"f14"`
			Market int    `json:"f13"`
		} `json:"diff"`
	} `json:"data"`
}

func newBrowserTransport() *http.Transport {
	return &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialer := &net.Dialer{Timeout: 10 * time.Second}
			conn, err := dialer.DialContext(ctx, "tcp", addr)
			if err != nil {
				return nil, err
			}
			host, _, _ := net.SplitHostPort(addr)
			tlsConn := utls.UClient(conn, &utls.Config{ServerName: host}, utls.HelloChrome_Auto)
			if err := tlsConn.Handshake(); err != nil {
				conn.Close()
				return nil, err
			}
			return tlsConn, nil
		},
		TLSHandshakeTimeout:  10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}
}
