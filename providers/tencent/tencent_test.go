package tencent

import (
	"encoding/json"
	"strings"
	"testing"

	gostox "github.com/T1anjiu/gostox"
)

func TestParseTencentQuote(t *testing.T) {
	fields := make([]string, tqMinFields+5)
	for i := range fields {
		fields[i] = "0"
	}
	fields[tqIdxName] = "浦发银行"
	fields[tqIdxCurrent] = "10.05"
	fields[tqIdxPrevClose] = "9.98"
	fields[tqIdxOpen] = "10.00"
	fields[tqIdxVolShou] = "1234"
	fields[tqIdxTimestamp] = "20240102150000"
	fields[tqIdxChange] = "0.07"
	fields[tqIdxChangePct] = "0.70"
	fields[tqIdxHigh] = "10.20"
	fields[tqIdxLow] = "9.95"
	fields[tqIdxAmountWan] = "9876"
	raw := strings.Join(fields, "~")

	code := gostox.StockCode{Market: gostox.MarketSH, Code: "600000"}
	q, err := parseTencentQuote(raw, code)
	if err != nil {
		t.Fatalf("parseTencentQuote: %v", err)
	}
	if q.Name != "浦发银行" {
		t.Errorf("name=%q", q.Name)
	}
	if q.Current != 10.05 || q.Open != 10.00 || q.PrevClose != 9.98 {
		t.Errorf("price mismatch: %+v", q)
	}
	if q.Volume != 1234*100 { // 手 → 股
		t.Errorf("volume=%d", q.Volume)
	}
	if q.Amount != 9876*10000 { // 万元 → 元
		t.Errorf("amount=%v", q.Amount)
	}
	if q.Change != 0.07 || q.ChangePct != 0.70 {
		t.Errorf("change=%v pct=%v", q.Change, q.ChangePct)
	}
	if q.Timestamp.IsZero() {
		t.Errorf("timestamp zero")
	}

	// 字段不足
	if _, err := parseTencentQuote("a~b~c", code); err == nil {
		t.Error("want error for short raw")
	}

	fields[tqIdxCurrent] = "bad"
	raw = strings.Join(fields, "~")
	if _, err := parseTencentQuote(raw, code); err == nil {
		t.Error("want error for invalid current")
	}
	fields[tqIdxCurrent] = "10.05"
	fields[tqIdxTimestamp] = "bad"
	raw = strings.Join(fields, "~")
	if _, err := parseTencentQuote(raw, code); err == nil {
		t.Error("want error for invalid timestamp")
	}
}

func TestToTencentKlineType(t *testing.T) {
	got, qfq, err := toTencentKlineType(gostox.KlinePeriodDay)
	if err != nil || got != "day" || qfq != "qfqday" {
		t.Errorf("day: got %q,%q,%v", got, qfq, err)
	}
	got, qfq, err = toTencentKlineType(gostox.KlinePeriod5Min)
	if err != nil || got != "m5" || qfq != "qfqm5" {
		t.Errorf("5min: got %q,%q,%v", got, qfq, err)
	}
	if _, _, err := toTencentKlineType(gostox.KlinePeriod(999)); err == nil {
		t.Error("want error for unknown period")
	}
}

func TestParseTencentKlineItem(t *testing.T) {
	code := gostox.StockCode{Market: gostox.MarketSH, Code: "600000"}
	item := []string{"2024-01-02", "10.00", "10.20", "10.30", "9.90", "12345", "987654"}
	k, err := parseTencentKlineItem(item, code, gostox.KlinePeriodDay)
	if err != nil {
		t.Fatalf("parseTencentKlineItem: %v", err)
	}
	if k.Open != 10.00 || k.Close != 10.20 || k.High != 10.30 || k.Low != 9.90 {
		t.Errorf("OHLC mismatch: %+v", k)
	}
	if k.Volume != 12345*100 {
		t.Errorf("volume=%d, want %d", k.Volume, 12345*100)
	}
	if k.Amount != 987654 {
		t.Errorf("amount=%f, want 987654", k.Amount)
	}
	if k.Timestamp.Format("2006-01-02") != "2024-01-02" {
		t.Errorf("timestamp=%v", k.Timestamp)
	}

	if _, err := parseTencentKlineItem([]string{"short"}, code, gostox.KlinePeriodDay); err == nil {
		t.Error("want error for short item")
	}
	if _, err := parseTencentKlineItem([]string{"bad-date", "10", "10", "10", "10", "1", "1"}, code, gostox.KlinePeriodDay); err == nil {
		t.Error("want error for bad timestamp")
	}
	if _, err := parseTencentKlineItem([]string{"2024-01-02", "bad", "10", "10", "10", "1", "1"}, code, gostox.KlinePeriodDay); err == nil {
		t.Error("want error for bad open")
	}

	item = []string{"2024-01-02", "10.00", "10.20", "10.30", "9.90", "12345"}
	k, err = parseTencentKlineItem(item, code, gostox.KlinePeriodDay)
	if err != nil {
		t.Fatalf("parseTencentKlineItem without amount: %v", err)
	}
	if k.Amount != 0 {
		t.Errorf("amount=%f, want 0 when omitted", k.Amount)
	}
}

func TestExtractKlines(t *testing.T) {
	raw := `{"code":0,"data":{"sh600000":{"qfqday":[["2026-04-22","9.71","9.61","9.73","9.59","685905"],["2026-04-23","9.60","9.54","9.65","9.51","806247"]],"qt":{"name":"浦发银行"},"prec":"9.72"}}}`
	var resp tencentKlineResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data := resp.Data["sh600000"]
	if data == nil {
		t.Fatal("no sh600000 data")
	}
	klines, err := extractKlines(data, "qfqday")
	if err != nil {
		t.Fatalf("extractKlines: %v", err)
	}
	if len(klines) != 2 {
		t.Fatalf("got %d klines, want 2", len(klines))
	}
	if klines[0][1] != "9.71" {
		t.Errorf("first open=%q", klines[0][1])
	}
}
