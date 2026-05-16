package tushare

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	gostox "github.com/T1anjiu/gostox"
	"github.com/T1anjiu/gostox/internal/testutil"
)

func TestParseTushareQuote(t *testing.T) {
	item := map[string]interface{}{
		"symbol":     "sh600000",
		"name":       "浦发银行",
		"close":      10.05,
		"open":       10.00,
		"high":       10.20,
		"low":        9.95,
		"pre_close":  9.98,
		"volume":     float64(123456),
		"amount":     9876543.21,
		"turnover":   0.7,
		"change":     0.07,
		"pct_change": 0.7,
		"trade_date": "2024-01-02",
	}

	q, err := parseTushareQuote(item)
	if err != nil {
		t.Fatalf("parseTushareQuote: %v", err)
	}
	if q.Name != "" {
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
	if q.Change != 0.07 || q.ChangePct != 0.7 {
		t.Errorf("change=%v pct=%v", q.Change, q.ChangePct)
	}
	if q.Timestamp.IsZero() {
		t.Errorf("timestamp zero")
	}
}

func TestToTusharePeriod(t *testing.T) {
	cases := map[gostox.KlinePeriod]string{
		gostox.KlinePeriod1Min:  "1",
		gostox.KlinePeriod5Min:  "5",
		gostox.KlinePeriod15Min: "15",
		gostox.KlinePeriod30Min: "30",
		gostox.KlinePeriod60Min: "60",
		gostox.KlinePeriodDay:   "D",
		gostox.KlinePeriodWeek:  "W",
		gostox.KlinePeriodMonth: "M",
	}
	for p, want := range cases {
		got, err := toTusharePeriod(p)
		if err != nil || got != want {
			t.Errorf("toTusharePeriod(%v)=%q,%v want %q", p, got, err, want)
		}
	}
	if _, err := toTusharePeriod(gostox.KlinePeriod(999)); err == nil {
		t.Error("want error for unknown period")
	}
}

func TestGetQuote_ReturnsPartialErrorWhenResponseMissesCodes(t *testing.T) {
	body := `{
		"code": 0,
		"msg": "success",
		"data": [
			{
				"symbol": "sh600000",
				"name": "浦发银行",
				"close": 10.05,
				"open": 10.00,
				"high": 10.20,
				"low": 9.95,
				"pre_close": 9.98,
				"volume": 123456,
				"amount": 9876543.21,
				"turnover": 0.7,
				"change": 0.07,
				"pct_change": 0.7,
				"trade_date": "2024-01-02"
			}
		]
	}`
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
