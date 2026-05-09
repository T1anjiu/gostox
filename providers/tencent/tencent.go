// Package tencent 封装腾讯财经行情接口。
package tencent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	gostox "github.com/T1anjiu/gostox"
)

const (
	quoteURL       = "https://qt.gtimg.cn/q="
	klineURL       = "https://web.ifzq.gtimg.cn/appstock/app/fqkline/get"
	userAgent      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"
	maxBodySize    = 4 << 20 // 4MB
	quoteChunkSize = 100     // 每批最多请求 100 只，避免 URL 过长
)

// 腾讯实时行情返回示例：
//   v_sh600000="1~浦发银行~600000~10.00~9.98~10.05~...~20240102150000~...";
// 字段以 ~ 分隔，共约 80+ 字段。
var tencentRegex = regexp.MustCompile(`v_((?:sh|sz)\d+)="(.+?)"`)

// 腾讯行情字段下标（仅列出使用到的）：
const (
	tqIdxName      = 1
	tqIdxCurrent   = 3
	tqIdxPrevClose = 4
	tqIdxOpen      = 5
	tqIdxVolShou   = 6  // 成交量（手）
	tqIdxTimestamp = 30 // yyyymmddHHMMSS
	tqIdxChange    = 31 // 涨跌额
	tqIdxChangePct = 32 // 涨跌幅(%)
	tqIdxHigh      = 33
	tqIdxLow       = 34
	tqIdxAmountWan = 37 // 成交额（万元）
	tqMinFields    = 40
)

// Provider 是腾讯财经数据源。
type Provider struct {
	client *http.Client
}

// Option 配置 Provider。
type Option func(*Provider)

// WithHTTPClient 自定义底层 HTTP client。
func WithHTTPClient(c *http.Client) Option {
	return func(p *Provider) { p.client = c }
}

// NewProvider 创建腾讯 Provider。
func NewProvider(opts ...Option) *Provider {
	p := &Provider{
		client: &http.Client{Timeout: 10 * time.Second},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name 返回 provider 名称。
func (p *Provider) Name() string { return "tencent" }

// GetQuote 查询实时行情。
// 超过 quoteChunkSize 的请求会自动分批发送。
func (p *Provider) GetQuote(ctx context.Context, codes ...gostox.StockCode) ([]*gostox.Quote, error) {
	if len(codes) == 0 {
		return nil, nil
	}

	requested := make(map[string]gostox.StockCode, len(codes))
	tencentCodes := make([]string, 0, len(codes))
	for _, c := range codes {
		requested[c.String()] = c
		tencentCodes = append(tencentCodes, c.TencentCode())
	}

	var quotes []*gostox.Quote
	var allParseErrs []error

	for i := 0; i < len(tencentCodes); i += quoteChunkSize {
		if err := ctx.Err(); err != nil {
			return quotes, fmt.Errorf("tencent quote: %w", err)
		}
		end := i + quoteChunkSize
		if end > len(tencentCodes) {
			end = len(tencentCodes)
		}
		chunk := tencentCodes[i:end]

		reqURL := quoteURL + strings.Join(chunk, ",")
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Referer", "https://gu.qq.com")
		req.Header.Set("User-Agent", userAgent)

		resp, err := p.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("tencent quote: %w", err)
		}

		body, err := func() ([]byte, error) {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("tencent quote http %d", resp.StatusCode)
			}
			return io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
		}()
		if err != nil {
			return nil, err
		}

		matches := tencentRegex.FindAllStringSubmatch(string(body), -1)
		for _, m := range matches {
			if len(m) < 3 {
				continue
			}
			code, err := gostox.ParseStockCode(m[1])
			if err != nil {
				allParseErrs = append(allParseErrs, fmt.Errorf("parse code %q: %w", m[1], err))
				continue
			}
			delete(requested, code.String())
			q, err := parseTencentQuote(m[2], code)
			if err != nil {
				allParseErrs = append(allParseErrs, fmt.Errorf("parse quote %s: %w", m[1], err))
				continue
			}
			quotes = append(quotes, q)
		}
	}
	for _, missing := range requested {
		allParseErrs = append(allParseErrs, fmt.Errorf("missing quote for %s", missing))
	}
	if len(allParseErrs) > 0 {
		return quotes, &gostox.PartialError{Failures: allParseErrs}
	}
	return quotes, nil
}

// GetKline 查询 K 线。
func (p *Provider) GetKline(ctx context.Context, code gostox.StockCode, period gostox.KlinePeriod, count int) ([]*gostox.Kline, error) {
	ktype, qfqKey, err := toTencentKlineType(period)
	if err != nil {
		return nil, fmt.Errorf("tencent kline: %w", err)
	}

	param := fmt.Sprintf("%s,%s,,,%d,qfq", code.TencentCode(), ktype, count)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, klineURL+"?param="+param, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Referer", "https://gu.qq.com")
	req.Header.Set("User-Agent", userAgent)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tencent kline: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tencent kline http %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return nil, fmt.Errorf("tencent kline read: %w", err)
	}

	var kresp tencentKlineResponse
	if err := json.Unmarshal(body, &kresp); err != nil {
		return nil, fmt.Errorf("tencent kline unmarshal: %w", err)
	}
	if kresp.Code != 0 {
		return nil, fmt.Errorf("tencent kline api code=%d", kresp.Code)
	}

	stockData, ok := kresp.Data[code.TencentCode()]
	if !ok {
		return nil, fmt.Errorf("tencent kline: no data for %s", code)
	}

	rawKlines, err := extractKlines(stockData, qfqKey, ktype)
	if err != nil {
		return nil, fmt.Errorf("tencent kline: %w", err)
	}

	klines := make([]*gostox.Kline, 0, len(rawKlines))
	var parseErrs []error
	for _, item := range rawKlines {
		k, err := parseTencentKlineItem(item, code, period)
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

// GetStockList 腾讯未提供稳定接口。
func (p *Provider) GetStockList(ctx context.Context) ([]*gostox.StockInfo, error) {
	return nil, gostox.ErrNotSupported
}

func parseTencentQuote(raw string, code gostox.StockCode) (*gostox.Quote, error) {
	fields := strings.Split(raw, "~")
	if len(fields) < tqMinFields {
		return nil, fmt.Errorf("tencent quote: insufficient fields (%d)", len(fields))
	}

	current, err := parseTencentFloat(fields[tqIdxCurrent], "current")
	if err != nil {
		return nil, err
	}
	prevClose, err := parseTencentFloat(fields[tqIdxPrevClose], "prev close")
	if err != nil {
		return nil, err
	}
	open, err := parseTencentFloat(fields[tqIdxOpen], "open")
	if err != nil {
		return nil, err
	}
	volShou, err := parseTencentInt(fields[tqIdxVolShou], "volume")
	if err != nil {
		return nil, err
	}
	high, err := parseTencentFloat(fields[tqIdxHigh], "high")
	if err != nil {
		return nil, err
	}
	low, err := parseTencentFloat(fields[tqIdxLow], "low")
	if err != nil {
		return nil, err
	}
	change, err := parseTencentFloat(fields[tqIdxChange], "change")
	if err != nil {
		return nil, err
	}
	changePct, err := parseTencentFloat(fields[tqIdxChangePct], "change pct")
	if err != nil {
		return nil, err
	}
	amountWan, err := parseTencentFloat(fields[tqIdxAmountWan], "amount")
	if err != nil {
		return nil, err
	}

	volume := volShou * 100    // 手 → 股
	amount := amountWan * 10000 // 万元 → 元

	tsStr := strings.TrimSpace(fields[tqIdxTimestamp])
	if len(tsStr) != 14 {
		return nil, fmt.Errorf("tencent quote: invalid timestamp %q", tsStr)
	}
	ts, err := time.ParseInLocation("20060102150405", tsStr, time.Local)
	if err != nil {
		return nil, fmt.Errorf("tencent quote: parse timestamp %q: %w", tsStr, err)
	}

	return &gostox.Quote{
		Code:      code,
		Name:      strings.TrimSpace(fields[tqIdxName]),
		Current:   current,
		Open:      open,
		PrevClose: prevClose,
		Close:     current,
		High:      high,
		Low:       low,
		Volume:    volume,
		Amount:    amount,
		Change:    change,
		ChangePct: changePct,
		Timestamp: ts,
	}, nil
}

func toTencentKlineType(p gostox.KlinePeriod) (string, string, error) {
	switch p {
	case gostox.KlinePeriod1Min:
		return "m1", "qfqm1", nil
	case gostox.KlinePeriod5Min:
		return "m5", "qfqm5", nil
	case gostox.KlinePeriod15Min:
		return "m15", "qfqm15", nil
	case gostox.KlinePeriod30Min:
		return "m30", "qfqm30", nil
	case gostox.KlinePeriod60Min:
		return "m60", "qfqm60", nil
	case gostox.KlinePeriodDay:
		return "day", "qfqday", nil
	case gostox.KlinePeriodWeek:
		return "week", "qfqweek", nil
	case gostox.KlinePeriodMonth:
		return "month", "qfqmonth", nil
	default:
		return "", "", fmt.Errorf("tencent: unsupported kline period %d", p)
	}
}

func parseTencentKlineItem(item []string, code gostox.StockCode, period gostox.KlinePeriod) (*gostox.Kline, error) {
	if len(item) < 6 {
		return nil, fmt.Errorf("tencent kline item: insufficient fields")
	}

	dateStr := item[0]
	openStr := item[1]
	closeStr := item[2]
	highStr := item[3]
	lowStr := item[4]
	volStr := item[5]

	// item[6] 为成交额（元），字段存在时解析，否则为 0。
	var amtStr string
	if len(item) > 6 {
		amtStr = item[6]
	}

	ts, err := parseTencentKlineTime(dateStr)
	if err != nil {
		return nil, err
	}

	open, err := parseTencentFloat(openStr, "open")
	if err != nil {
		return nil, err
	}
	close_, err := parseTencentFloat(closeStr, "close")
	if err != nil {
		return nil, err
	}
	high, err := parseTencentFloat(highStr, "high")
	if err != nil {
		return nil, err
	}
	low, err := parseTencentFloat(lowStr, "low")
	if err != nil {
		return nil, err
	}
	vol, err := parseTencentFloat(volStr, "volume")
	if err != nil {
		return nil, err
	}
	amt, err := parseTencentOptionalFloat(amtStr, "amount")
	if err != nil {
		return nil, err
	}

	return &gostox.Kline{
		Code:      code,
		Open:      open,
		Close:     close_,
		High:      high,
		Low:       low,
		Volume:    int64(math.Round(vol * 100)), // 单位为手，×100 转为股，与其他 provider 统一
		Amount:    amt,
		Timestamp: ts,
		Period:    period,
	}, nil
}

func parseTencentFloat(raw, field string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, fmt.Errorf("tencent: parse %s %q: %w", field, raw, err)
	}
	return v, nil
}

func parseTencentOptionalFloat(raw, field string) (float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	return parseTencentFloat(raw, field)
}

func parseTencentInt(raw, field string) (int64, error) {
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("tencent: parse %s %q: %w", field, raw, err)
	}
	return v, nil
}

func parseTencentKlineTime(raw string) (time.Time, error) {
	ts, err := time.Parse("2006-01-02", raw)
	if err == nil {
		return ts, nil
	}
	ts, err = time.Parse("2006-01-02 15:04", raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("tencent kline: parse timestamp %q: %w", raw, err)
	}
	return ts, nil
}

type tencentKlineResponse struct {
	Code  int                                          `json:"code"`
	Data  map[string]map[string]json.RawMessage        `json:"data"`
}

func extractKlines(data map[string]json.RawMessage, keys ...string) ([][]string, error) {
	for _, key := range keys {
		raw, ok := data[key]
		if !ok {
			continue
		}
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("tencent kline: unmarshal key %q: %w", key, err)
		}
		result := make([][]string, 0, len(items))
		var parseErrs []error
		for i, item := range items {
			var arr []string
			if err := json.Unmarshal(item, &arr); err != nil {
				var mixed []interface{}
				if err2 := json.Unmarshal(item, &mixed); err2 != nil {
					parseErrs = append(parseErrs, fmt.Errorf("item %d: unmarshal: %w then %w", i, err, err2))
					continue
				}
				arr = make([]string, 0, len(mixed))
				for _, v := range mixed {
					if v == nil {
						arr = append(arr, "")
					} else {
						arr = append(arr, fmt.Sprint(v))
					}
				}
			}
			result = append(result, arr)
		}
		if len(parseErrs) > 0 {
			return result, &gostox.PartialError{Failures: parseErrs}
		}
		return result, nil
	}
	return nil, fmt.Errorf("none of keys %v found", keys)
}
