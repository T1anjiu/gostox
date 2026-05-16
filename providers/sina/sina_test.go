package sina

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	gostox "github.com/T1anjiu/gostox"
	"github.com/T1anjiu/gostox/internal/testutil"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

func TestParseSinaQuote(t *testing.T) {
	// 构造一个满足最少字段数的 raw 字符串（32 字段）。
	fields := make([]string, 33)
	fields[sqIdxName] = "浦发银行"
	fields[sqIdxOpen] = "10.00"
	fields[sqIdxPrevClose] = "9.98"
	fields[sqIdxCurrent] = "10.05"
	fields[sqIdxHigh] = "10.20"
	fields[sqIdxLow] = "9.95"
	fields[sqIdxVolume] = "123456"
	fields[sqIdxAmount] = "9876543.21"
	fields[sqIdxDate] = "2024-01-02"
	fields[sqIdxTime] = "15:00:00"
	raw := strings.Join(fields, ",")

	code := gostox.StockCode{Market: gostox.MarketSH, Code: "600000"}
	q, err := parseSinaQuote(raw, code)
	if err != nil {
		t.Fatalf("parseSinaQuote: %v", err)
	}
	if q.Name != "浦发银行" {
		t.Errorf("name=%q", q.Name)
	}
	if q.Current != 10.05 || q.Open != 10.00 || q.PrevClose != 9.98 {
		t.Errorf("price mismatch: %+v", q)
	}
	if q.High != 10.20 || q.Low != 9.95 {
		t.Errorf("hi/lo mismatch: %+v", q)
	}
	if q.Volume != 123456 {
		t.Errorf("volume=%d", q.Volume)
	}
	if gotChange := q.Current - q.PrevClose; q.Change != gotChange {
		t.Errorf("change=%v want %v", q.Change, gotChange)
	}
	if q.ChangePct <= 0 {
		t.Errorf("changePct=%v", q.ChangePct)
	}
	if q.Timestamp.IsZero() {
		t.Errorf("timestamp zero")
	}

	// 字段不足
	if _, err := parseSinaQuote("a,b,c", code); err == nil {
		t.Error("want error for short raw")
	}

	fields[sqIdxCurrent] = "bad"
	raw = strings.Join(fields, ",")
	if _, err := parseSinaQuote(raw, code); err == nil {
		t.Error("want error for invalid current")
	}
	fields[sqIdxCurrent] = "10.05"
	fields[sqIdxTime] = "bad"
	raw = strings.Join(fields, ",")
	if _, err := parseSinaQuote(raw, code); err == nil {
		t.Error("want error for invalid timestamp")
	}
}

func TestToSinaScale(t *testing.T) {
	cases := map[gostox.KlinePeriod]int{
		gostox.KlinePeriod5Min:  5,
		gostox.KlinePeriod15Min: 15,
		gostox.KlinePeriod30Min: 30,
		gostox.KlinePeriod60Min: 60,
		gostox.KlinePeriodDay:   240,
		gostox.KlinePeriodWeek:  1200,
		gostox.KlinePeriodMonth: 7200,
	}
	for p, want := range cases {
		got, err := toSinaScale(p)
		if err != nil || got != want {
			t.Errorf("toSinaScale(%v)=%d,%v want %d", p, got, err, want)
		}
	}
	if _, err := toSinaScale(gostox.KlinePeriod1Min); !errors.Is(err, gostox.ErrNotSupported) {
		t.Errorf("1min: got err=%v, want ErrNotSupported", err)
	}
	if _, err := toSinaScale(gostox.KlinePeriod(999)); err == nil {
		t.Error("want error for unknown period")
	}
}

func TestDecodeGBK(t *testing.T) {
	src := "浦发银行 涨 0.5%"
	gbk, _, err := transform.String(simplifiedchinese.GBK.NewEncoder(), src)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := decodeGBK([]byte(gbk))
	if err != nil {
		t.Fatalf("decodeGBK: %v", err)
	}
	if got != src {
		t.Errorf("got %q want %q", got, src)
	}
}

func TestParseSinaKlineItem(t *testing.T) {
	code := gostox.StockCode{Market: gostox.MarketSH, Code: "600000"}
	item := sinaKlineItem{Day: "2024-01-02", Open: "10.0", High: "10.2", Low: "9.9", Close: "10.1", Volume: "12345"}
	k, err := parseSinaKlineItem(item, code, gostox.KlinePeriodDay)
	if err != nil {
		t.Fatalf("parseSinaKlineItem: %v", err)
	}
	if k.Volume != 12345 || k.Close != 10.1 {
		t.Fatalf("unexpected kline: %+v", k)
	}
	if k.Amount != 0 {
		t.Errorf("sina API does not return Amount, expected 0, got %f", k.Amount)
	}

	item.Volume = "bad"
	if _, err := parseSinaKlineItem(item, code, gostox.KlinePeriodDay); err == nil {
		t.Error("want error for invalid volume")
	}
	item.Volume = "12345"
	item.Day = "bad"
	if _, err := parseSinaKlineItem(item, code, gostox.KlinePeriodDay); err == nil {
		t.Error("want error for invalid day")
	}
}

func TestGetQuote_ReturnsPartialErrorWhenResponseMissesCodes(t *testing.T) {
	body := `var hq_str_sh600000="浦发银行,10.00,9.98,10.05,10.20,9.95,0,0,123456,9876543.21,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,2024-01-02,15:00:00,00";`
	p := NewProvider(WithHTTPClient(&http.Client{Transport: testutil.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}))

	quotes, err := p.GetQuote(context.Background(),
		gostox.StockCode{Market: gostox.MarketSH, Code: "600000"},
		gostox.StockCode{Market: gostox.MarketSZ, Code: "000001"},
	)
	if len(quotes) != 1 || quotes[0].Code.String() != "sh600000" {
		t.Fatalf("unexpected quotes: %+v", quotes)
	}
	var pe *gostox.PartialError
	if !errors.As(err, &pe) {
		t.Fatalf("want PartialError, got %v", err)
	}
	if len(pe.Failures) != 1 || !strings.Contains(pe.Failures[0].Error(), "sz000001") {
		t.Fatalf("unexpected failures: %+v", pe.Failures)
	}
}



