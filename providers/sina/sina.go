// Package sina 封装新浪财经行情接口。
package sina

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	gostox "github.com/T1anjiu/gostox"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

const (
	quoteURL       = "https://hq.sinajs.cn/list="
	klineURL       = "https://money.finance.sina.com.cn/quotes_service/api/json_v2.php/CN_MarketData.getKLineData"
	userAgent      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"
	maxBodySize    = 4 << 20 // 4MB
	quoteChunkSize = 100     // 每批最多请求 100 只，避免 URL 过长
)

// 新浪实时行情返回格式示例（GBK 编码）：
//   var hq_str_sh600000="浦发银行,10.00,9.98,10.05,10.10,9.95,...,2024-01-02,15:00:00,00";
// 字段以逗号分隔，共 33 个字段（含末尾空串）。
var hqRegex = regexp.MustCompile(`var hq_str_((?:sh|sz)\d+)="(.*?)";`)

// 字段下标（部分）：
const (
	sqIdxName      = 0  // 股票名称
	sqIdxOpen      = 1  // 今开
	sqIdxPrevClose = 2  // 昨收
	sqIdxCurrent   = 3  // 当前价
	sqIdxHigh      = 4  // 最高
	sqIdxLow       = 5  // 最低
	sqIdxVolume    = 8  // 成交量（股）
	sqIdxAmount    = 9  // 成交额（元）
	sqIdxDate      = 30 // 日期
	sqIdxTime      = 31 // 时间
	sqMinFields    = 32
)

// Provider 是新浪财经数据源。
type Provider struct {
	client *http.Client
}

// Option 配置 Provider。
type Option func(*Provider)

// WithHTTPClient 自定义底层 HTTP client。
func WithHTTPClient(c *http.Client) Option {
	return func(p *Provider) { p.client = c }
}

// NewProvider 创建新浪 Provider。
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
func (p *Provider) Name() string { return "sina" }

// GetQuote 查询实时行情。响应为 GBK，需要解码后再解析。
// 超过 quoteChunkSize 的请求会自动分批发送。
func (p *Provider) GetQuote(ctx context.Context, codes ...gostox.StockCode) ([]*gostox.Quote, error) {
	if len(codes) == 0 {
		return nil, nil
	}

	requested := make(map[string]gostox.StockCode, len(codes))
	sinaCodes := make([]string, 0, len(codes))
	for _, c := range codes {
		requested[c.String()] = c
		sinaCodes = append(sinaCodes, c.SinaCode())
	}

	now := time.Now()
	var quotes []*gostox.Quote
	var allParseErrs []error

	for i := 0; i < len(sinaCodes); i += quoteChunkSize {
		end := i + quoteChunkSize
		if end > len(sinaCodes) {
			end = len(sinaCodes)
		}
		chunk := sinaCodes[i:end]

		reqURL := quoteURL + strings.Join(chunk, ",")
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Referer", "https://finance.sina.com.cn")
		req.Header.Set("User-Agent", userAgent)

		resp, err := p.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("sina quote: %w", err)
		}

		body, err := func() ([]byte, error) {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("sina quote http %d", resp.StatusCode)
			}
			return io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
		}()
		if err != nil {
			return nil, err
		}

		text, err := decodeGBK(body)
		if err != nil {
			return nil, fmt.Errorf("sina quote decode: %w", err)
		}

		matches := hqRegex.FindAllStringSubmatch(text, -1)
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
			q, err := parseSinaQuote(m[2], code)
			if err != nil {
				allParseErrs = append(allParseErrs, fmt.Errorf("parse quote %s: %w", m[1], err))
				continue
			}
			quotes = append(quotes, q)
		}
	}
	_ = now
	for _, missing := range requested {
		allParseErrs = append(allParseErrs, fmt.Errorf("missing quote for %s", missing))
	}
	if len(allParseErrs) > 0 {
		return quotes, &gostox.PartialError{Failures: allParseErrs}
	}
	return quotes, nil
}

// GetKline 查询 K 线。新浪 K 线响应为 UTF-8 JSON，无需解码。
func (p *Provider) GetKline(ctx context.Context, code gostox.StockCode, period gostox.KlinePeriod, count int) ([]*gostox.Kline, error) {
	scale, err := toSinaScale(period)
	if err != nil {
		return nil, fmt.Errorf("sina kline: %w", err)
	}

	params := url.Values{}
	params.Set("symbol", code.SinaCode())
	params.Set("scale", strconv.Itoa(scale))
	params.Set("ma", "no")
	params.Set("datalen", strconv.Itoa(count))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, klineURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Referer", "https://finance.sina.com.cn")
	req.Header.Set("User-Agent", userAgent)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sina kline: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sina kline http %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return nil, fmt.Errorf("sina kline read: %w", err)
	}

	var items []sinaKlineItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("sina kline unmarshal: %w", err)
	}

	klines := make([]*gostox.Kline, 0, len(items))
	var parseErrs []error
	for _, item := range items {
		k, err := parseSinaKlineItem(item, code, period)
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

// GetStockList 新浪未提供稳定的全量接口，此处不支持。
func (p *Provider) GetStockList(ctx context.Context) ([]*gostox.StockInfo, error) {
	return nil, gostox.ErrNotSupported
}

func parseSinaQuote(raw string, code gostox.StockCode) (*gostox.Quote, error) {
	fields := strings.Split(raw, ",")
	if len(fields) < sqMinFields {
		return nil, fmt.Errorf("sina quote: insufficient fields (%d)", len(fields))
	}

	open, err := parseSinaFloat(fields[sqIdxOpen], "open")
	if err != nil {
		return nil, err
	}
	prevClose, err := parseSinaFloat(fields[sqIdxPrevClose], "prev close")
	if err != nil {
		return nil, err
	}
	current, err := parseSinaFloat(fields[sqIdxCurrent], "current")
	if err != nil {
		return nil, err
	}
	high, err := parseSinaFloat(fields[sqIdxHigh], "high")
	if err != nil {
		return nil, err
	}
	low, err := parseSinaFloat(fields[sqIdxLow], "low")
	if err != nil {
		return nil, err
	}
	volume, err := parseSinaInt(fields[sqIdxVolume], "volume")
	if err != nil {
		return nil, err
	}
	amount, err := parseSinaFloat(fields[sqIdxAmount], "amount")
	if err != nil {
		return nil, err
	}

	dateStr := strings.TrimSpace(fields[sqIdxDate])
	timeStr := strings.TrimSpace(fields[sqIdxTime])
	ts, err := time.Parse("2006-01-02 15:04:05", dateStr+" "+timeStr)
	if err != nil {
		return nil, fmt.Errorf("sina quote: parse timestamp %q %q: %w", dateStr, timeStr, err)
	}

	change := current - prevClose
	var changePct float64
	if prevClose > 0 {
		changePct = change / prevClose * 100
	}

	return &gostox.Quote{
		Code:      code,
		Name:      strings.TrimSpace(fields[sqIdxName]),
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

func parseSinaKlineItem(item sinaKlineItem, code gostox.StockCode, period gostox.KlinePeriod) (*gostox.Kline, error) {
	ts, err := parseSinaKlineTime(item.Day)
	if err != nil {
		return nil, err
	}
	open, err := parseSinaFloat(item.Open, "open")
	if err != nil {
		return nil, err
	}
	high, err := parseSinaFloat(item.High, "high")
	if err != nil {
		return nil, err
	}
	low, err := parseSinaFloat(item.Low, "low")
	if err != nil {
		return nil, err
	}
	close_, err := parseSinaFloat(item.Close, "close")
	if err != nil {
		return nil, err
	}
	vol, err := parseSinaInt(item.Volume, "volume")
	if err != nil {
		return nil, err
	}

	return &gostox.Kline{
		Code:      code,
		Open:      open,
		Close:     close_,
		High:      high,
		Low:       low,
		Volume:    vol,
		Timestamp: ts,
		Period:    period,
	}, nil
}

// 新浪 K 线 scale 字段：分钟级直接用分钟数，日=240，周=1200，月=7200。
func toSinaScale(p gostox.KlinePeriod) (int, error) {
	switch p {
	case gostox.KlinePeriod1Min:
		return 0, gostox.ErrNotSupported
	case gostox.KlinePeriod5Min:
		return 5, nil
	case gostox.KlinePeriod15Min:
		return 15, nil
	case gostox.KlinePeriod30Min:
		return 30, nil
	case gostox.KlinePeriod60Min:
		return 60, nil
	case gostox.KlinePeriodDay:
		return 240, nil
	case gostox.KlinePeriodWeek:
		return 1200, nil
	case gostox.KlinePeriodMonth:
		return 7200, nil
	default:
		return 0, fmt.Errorf("sina: unsupported kline period %d", p)
	}
}

// decodeGBK 将 GBK 字节转为 UTF-8 字符串。
func decodeGBK(b []byte) (string, error) {
	r := transform.NewReader(io.LimitReader(bytes.NewReader(b), maxBodySize), simplifiedchinese.GBK.NewDecoder())
	out, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func parseSinaFloat(raw, field string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, fmt.Errorf("sina quote: parse %s %q: %w", field, raw, err)
	}
	return v, nil
}

func parseSinaInt(raw, field string) (int64, error) {
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("sina quote: parse %s %q: %w", field, raw, err)
	}
	return v, nil
}

func parseSinaKlineTime(raw string) (time.Time, error) {
	ts, err := time.Parse("2006-01-02", raw)
	if err == nil {
		return ts, nil
	}
	ts, err = time.Parse("2006-01-02 15:04:05", raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("sina kline: parse timestamp %q: %w", raw, err)
	}
	return ts, nil
}

type sinaKlineItem struct {
	Day    string `json:"day"`
	Open   string `json:"open"`
	High   string `json:"high"`
	Low    string `json:"low"`
	Close  string `json:"close"`
	Volume string `json:"volume"`
}
