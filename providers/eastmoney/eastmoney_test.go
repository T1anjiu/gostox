package eastmoney

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gostox "github.com/T1anjiu/GoStox"
)

func TestToKlineType(t *testing.T) {
	cases := map[gostox.KlinePeriod]int{
		gostox.KlinePeriod1Min:   1,
		gostox.KlinePeriod5Min:   5,
		gostox.KlinePeriod15Min:  15,
		gostox.KlinePeriod30Min:  30,
		gostox.KlinePeriod60Min:  60,
		gostox.KlinePeriodDay:    101,
		gostox.KlinePeriodWeek:   102,
		gostox.KlinePeriodMonth:  103,
	}
	for p, want := range cases {
		got, err := toKlineType(p)
		if err != nil || got != want {
			t.Errorf("toKlineType(%v)=%d,%v want %d", p, got, err, want)
		}
	}
	if _, err := toKlineType(gostox.KlinePeriod(999)); err == nil {
		t.Error("want error for unknown period")
	}
}

func TestMarketPrefix(t *testing.T) {
	if got, err := marketPrefix(1); err != nil || got != "sh" {
		t.Errorf("marketPrefix(1)=%q want sh", got)
	}
	if got, err := marketPrefix(0); err != nil || got != "sz" {
		t.Errorf("marketPrefix(0)=%q want sz", got)
	}
	if _, err := marketPrefix(2); err == nil {
		t.Error("want error for unknown market")
	}
}

func TestParseKlineLine(t *testing.T) {
	code := gostox.StockCode{Market: gostox.MarketSH, Code: "600000"}
	line := "2024-01-02,10.00,10.20,10.30,9.90,12345,987654,1.0,2.0,3.0,4.0"

	k, err := parseKlineLine(line, code, gostox.KlinePeriodDay)
	if err != nil {
		t.Fatalf("parseKlineLine: %v", err)
	}
	if k.Open != 10.00 || k.Close != 10.20 || k.High != 10.30 || k.Low != 9.90 {
		t.Errorf("OHLC mismatch: %+v", k)
	}
	if k.Volume != 12345*100 || k.Amount != 987654 {
		t.Errorf("vol/amt mismatch: %+v", k)
	}
	if k.Timestamp.Format("2006-01-02") != "2024-01-02" {
		t.Errorf("timestamp=%v", k.Timestamp)
	}
	if k.Period != gostox.KlinePeriodDay {
		t.Errorf("period=%v", k.Period)
	}

	// 字段过少
	if _, err := parseKlineLine("2024-01-02,1,2,3", code, gostox.KlinePeriodDay); err == nil {
		t.Error("want error for short line")
	}

	// 非法时间
	if _, err := parseKlineLine("bad-date,1,2,3,4,5,6", code, gostox.KlinePeriodDay); err == nil {
		t.Error("want error for bad date")
	}
	if _, err := parseKlineLine("2024-01-02,bad,2,3,4,5,6", code, gostox.KlinePeriodDay); err == nil {
		t.Error("want error for bad open")
	}
}

func TestDoGet_LimitReader(t *testing.T) {
	bigBody := strings.Repeat("x", 5<<20) // 5MB，超过 maxBodySize=4MB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(bigBody))
	}))
	defer srv.Close()

	p := NewProvider()
	// 直接调用 doGet 测试 LimitReader
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := p.client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(body) > maxBodySize {
		t.Errorf("body size %d exceeds maxBodySize %d", len(body), maxBodySize)
	}
	if len(body) != maxBodySize {
		t.Errorf("body size=%d want=%d (should be truncated at maxBodySize)", len(body), maxBodySize)
	}
}
