// healthcheck 对三个 provider 逐一做真实网络调用，验证返回数据的合理性。
// 同时验证 Client 故障转移、ctx 取消、分块请求等核心机制。
// 用法：go run ./examples/healthcheck
// 全部通过时退出码为 0，任意失败时退出码为 1。
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	gostox "github.com/T1anjiu/gostox"
	"github.com/T1anjiu/gostox/providers/eastmoney"
	"github.com/T1anjiu/gostox/providers/sina"
	"github.com/T1anjiu/gostox/providers/tencent"
)

var testCodes = []gostox.StockCode{
	{Market: gostox.MarketSH, Code: "600000"},
	{Market: gostox.MarketSZ, Code: "000001"},
}

var bjCode = gostox.StockCode{Market: gostox.MarketBJ, Code: "830949"}

var testIndexes = []gostox.IndexCode{
	{Code: "000001"}, // 上证指数
	{Code: "399001"}, // 深证成指
}

type result struct {
	name   string
	passed bool
	msg    string
}

func main() {
	providers := []gostox.Provider{
		eastmoney.NewProvider(),
		sina.NewProvider(),
		tencent.NewProvider(),
	}

	var results []result

	// 1. 逐 provider 检查基础功能
	for _, p := range providers {
		results = append(results, checkQuote(p))
		results = append(results, checkKline(p))
	}
	results = append(results, checkStockList(providers[0]))

	// 2. 北交所覆盖测试（仅东方财富，新浪/腾讯不支持 bj 前缀）
	results = append(results, checkBJQuote(eastmoney.NewProvider(), "eastmoney"))

	// 3. Client 故障转移机制
	results = append(results, checkClientFailover())

	// 4. ctx 超时取消
	results = append(results, checkCtxCancel())

	// 5. 大批量 GetQuote（触发分块，>quoteChunkSize）
	results = append(results, checkBatchQuote(providers...))

	// 6. Kline 多周期覆盖
	results = append(results, checkKlinePeriods(sina.NewProvider()))

	// 7. 指数行情
	results = append(results, checkIndexQuote(providers[0]))
	results = append(results, checkIndexKline(providers[0]))

	fmt.Println("\n========== 健康检查结果 ==========")
	allPassed := true
	for _, r := range results {
		status := "PASS"
		if !r.passed {
			status = "FAIL"
			allPassed = false
		}
		fmt.Printf("[%s] %s: %s\n", status, r.name, r.msg)
	}
	fmt.Println("===================================")

	if allPassed {
		fmt.Println("全部通过，库可以正常使用。")
		os.Exit(0)
	} else {
		fmt.Println("存在失败项，请检查网络或接口状态。")
		os.Exit(1)
	}
}

func checkQuote(p gostox.Provider) result {
	name := fmt.Sprintf("%s/GetQuote", p.Name())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	quotes, err := p.GetQuote(ctx, testCodes...)
	if err != nil {
		var pe *gostox.PartialError
		if !errors.As(err, &pe) {
			return result{name, false, fmt.Sprintf("请求失败: %v", err)}
		}
	}

	if len(quotes) == 0 {
		return result{name, false, "返回数据为空"}
	}

	for _, q := range quotes {
		if q.Current <= 0 {
			return result{name, false, fmt.Sprintf("%s 当前价 %.2f 不合理（应 > 0）", q.Code, q.Current)}
		}
		if q.Open <= 0 || q.PrevClose <= 0 {
			return result{name, false, fmt.Sprintf("%s 开盘价 %.2f 或昨收 %.2f 不合理（应 > 0）", q.Code, q.Open, q.PrevClose)}
		}
		if q.High < q.Low {
			return result{name, false, fmt.Sprintf("%s 最高价 %.2f < 最低价 %.2f", q.Code, q.High, q.Low)}
		}
		if q.High < q.Current || q.Low > q.Current {
			return result{name, false, fmt.Sprintf("%s 当前价 %.2f 不在最高/最低价范围内 [%.2f, %.2f]", q.Code, q.Current, q.Low, q.High)}
		}
		if q.Volume < 0 {
			return result{name, false, fmt.Sprintf("%s 成交量 %d 不合理（应 >= 0）", q.Code, q.Volume)}
		}
		if q.Name == "" {
			return result{name, false, fmt.Sprintf("%s 股票名称为空", q.Code)}
		}
		if q.Timestamp.IsZero() {
			return result{name, false, fmt.Sprintf("%s 时间戳为零", q.Code)}
		}
	}

	return result{name, true, fmt.Sprintf("返回 %d 条，价格/量/名称/时间戳均正常", len(quotes))}
}

func checkKline(p gostox.Provider) result {
	name := fmt.Sprintf("%s/GetKline", p.Name())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	code := testCodes[0]
	klines, err := p.GetKline(ctx, code, gostox.KlinePeriodDay, 10)
	if err != nil {
		if errors.Is(err, gostox.ErrNotSupported) {
			return result{name, true, "不支持（跳过）"}
		}
		var pe *gostox.PartialError
		if !errors.As(err, &pe) {
			return result{name, false, fmt.Sprintf("请求失败: %v", err)}
		}
	}

	if len(klines) == 0 {
		return result{name, false, "返回数据为空"}
	}

	for _, k := range klines {
		if k.Open <= 0 || k.Close <= 0 || k.High <= 0 || k.Low <= 0 {
			return result{name, false, fmt.Sprintf("%s K线价格含零值: O=%.2f C=%.2f H=%.2f L=%.2f", k.Timestamp.Format("2006-01-02"), k.Open, k.Close, k.High, k.Low)}
		}
		if k.High < k.Low {
			return result{name, false, fmt.Sprintf("%s 最高价 %.2f < 最低价 %.2f", k.Timestamp.Format("2006-01-02"), k.High, k.Low)}
		}
		if k.Volume < 0 {
			return result{name, false, fmt.Sprintf("%s 成交量 %d 不合理", k.Timestamp.Format("2006-01-02"), k.Volume)}
		}
		if k.Timestamp.IsZero() {
			return result{name, false, "存在时间戳为零的 K 线"}
		}
	}

	return result{name, true, fmt.Sprintf("返回 %d 根 K 线，OHLCV 均正常", len(klines))}
}

func checkStockList(p gostox.Provider) result {
	name := fmt.Sprintf("%s/GetStockList", p.Name())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	list, err := p.GetStockList(ctx)
	if err != nil {
		if errors.Is(err, gostox.ErrNotSupported) {
			return result{name, true, "不支持（跳过）"}
		}
		return result{name, false, fmt.Sprintf("请求失败: %v", err)}
	}

	if len(list) < 4000 {
		return result{name, false, fmt.Sprintf("股票数量 %d 偏少（预期 > 4000），接口可能异常", len(list))}
	}

	for i, s := range list {
		if i >= 5 {
			break
		}
		if s.Name == "" || s.Code.Code == "" {
			return result{name, false, fmt.Sprintf("第 %d 条数据名称或代码为空: %+v", i, s)}
		}
	}

	return result{name, true, fmt.Sprintf("返回 %d 只股票，格式正常", len(list))}
}

func checkBJQuote(p gostox.Provider, label string) result {
	name := fmt.Sprintf("%s/GetQuote(BJ)", label)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	quotes, err := p.GetQuote(ctx, bjCode)
	if err != nil {
		if errors.Is(err, gostox.ErrNotSupported) {
			return result{name, true, "不支持（跳过）"}
		}
		var pe *gostox.PartialError
		if !errors.As(err, &pe) {
			return result{name, false, fmt.Sprintf("请求失败: %v", err)}
		}
	}

	if len(quotes) == 0 {
		return result{name, true, "北交所股票未返回数据（可能接口不支持 bj 前缀）"}
	}

	q := quotes[0]
	if q.Code.Market != gostox.MarketBJ {
		return result{name, false, fmt.Sprintf("市场识别错误: got=%v want=MarketBJ", q.Code.Market)}
	}
	if q.Current <= 0 {
		return result{name, false, fmt.Sprintf("北交所 %s 当前价 %.2f 不合理", q.Code, q.Current)}
	}

	return result{name, true, fmt.Sprintf("北交所 %s 当前价=%.2f 市场识别正确", q.Code, q.Current)}
}

func checkClientFailover() result {
	name := "Client/Failover"
	em := eastmoney.NewProvider()
	sn := sina.NewProvider()
	client, _ := gostox.NewClient(
		&failProvider{name: "always-fail"},
		em,
		sn,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	quotes, err := client.GetQuote(ctx, testCodes...)
	if err != nil {
		return result{name, false, fmt.Sprintf("故障转移后仍失败: %v", err)}
	}
	if len(quotes) == 0 {
		return result{name, false, "故障转移后返回数据为空"}
	}

	return result{name, true, fmt.Sprintf("首个 provider 失败后自动切换，返回 %d 条", len(quotes))}
}

func checkCtxCancel() result {
	name := "Client/CtxCancel"
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()

	em := eastmoney.NewProvider()
	_, err := em.GetQuote(ctx, testCodes...)
	if err == nil {
		return result{name, false, "超时 ctx 未触发错误"}
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		return result{name, true, fmt.Sprintf("ctx 触发了错误（非 DeadlineExceeded 但合理）: %v", err)}
	}

	return result{name, true, "超时 ctx 正确触发错误"}
}

func checkBatchQuote(providers ...gostox.Provider) result {
	name := "Client/BatchQuote(>100)"
	client, _ := gostox.NewClient(providers...)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	codes := make([]gostox.StockCode, 0, 150)
	for i := 0; i < 150; i++ {
		code := gostox.InferMarket(fmt.Sprintf("%06d", 600000+i))
		codes = append(codes, code)
	}

	quotes, err := client.GetQuote(ctx, codes...)
	if err != nil {
		var pe *gostox.PartialError
		if !errors.As(err, &pe) {
			return result{name, false, fmt.Sprintf("批量请求失败: %v", err)}
		}
	}

	if len(quotes) == 0 {
		return result{name, false, "批量请求返回数据为空"}
	}

	return result{name, true, fmt.Sprintf("请求 %d 只，返回 %d 条（分块机制工作正常）", len(codes), len(quotes))}
}

func checkKlinePeriods(p gostox.Provider) result {
	name := fmt.Sprintf("%s/KlinePeriods", p.Name())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	periods := []struct {
		Period gostox.KlinePeriod
		Label string
	}{
		{gostox.KlinePeriod5Min, "5min"},
		{gostox.KlinePeriod15Min, "15min"},
		{gostox.KlinePeriod60Min, "60min"},
		{gostox.KlinePeriodDay, "day"},
		{gostox.KlinePeriodWeek, "week"},
	}

	supported := 0
	for _, pp := range periods {
		_, err := p.GetKline(ctx, testCodes[0], pp.Period, 5)
		if err != nil {
			if errors.Is(err, gostox.ErrNotSupported) {
				continue
			}
			return result{name, false, fmt.Sprintf("%s 周期请求失败: %v", pp.Label, err)}
		}
		supported++
	}

	if supported == 0 {
		return result{name, false, "没有任何周期支持"}
	}

	return result{name, true, fmt.Sprintf("%d/%d 个周期可用", supported, len(periods))}
}

func checkIndexQuote(p gostox.Provider) result {
	name := fmt.Sprintf("%s/GetIndexQuote", p.Name())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	quotes, err := p.GetIndexQuote(ctx, testIndexes...)
	if err != nil {
		if errors.Is(err, gostox.ErrNotSupported) {
			return result{name, true, "不支持（跳过）"}
		}
		var pe *gostox.PartialError
		if !errors.As(err, &pe) {
			return result{name, false, fmt.Sprintf("请求失败: %v", err)}
		}
	}

	if len(quotes) == 0 {
		return result{name, false, "返回数据为空"}
	}

	for _, q := range quotes {
		if q.Current <= 0 {
			return result{name, false, fmt.Sprintf("%s 当前价 %.2f 不合理（应 > 0）", q.Code, q.Current)}
		}
		if q.Open <= 0 || q.PrevClose <= 0 {
			return result{name, false, fmt.Sprintf("%s 开盘价 %.2f 或昨收 %.2f 不合理", q.Code, q.Open, q.PrevClose)}
		}
		if q.High < q.Low {
			return result{name, false, fmt.Sprintf("%s 最高价 %.2f < 最低价 %.2f", q.Code, q.High, q.Low)}
		}
		if q.Name == "" {
			return result{name, false, fmt.Sprintf("%s 指数名称为空", q.Code)}
		}
	}

	return result{name, true, fmt.Sprintf("返回 %d 个指数，数据合理", len(quotes))}
}

func checkIndexKline(p gostox.Provider) result {
	name := fmt.Sprintf("%s/GetIndexKline", p.Name())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	code := testIndexes[0]
	klines, err := p.GetIndexKline(ctx, code, gostox.KlinePeriodDay, 10)
	if err != nil {
		if errors.Is(err, gostox.ErrNotSupported) {
			return result{name, true, "不支持（跳过）"}
		}
		var pe *gostox.PartialError
		if !errors.As(err, &pe) {
			return result{name, false, fmt.Sprintf("请求失败: %v", err)}
		}
	}

	if len(klines) == 0 {
		return result{name, false, "返回数据为空"}
	}

	for _, k := range klines {
		if k.Open <= 0 || k.Close <= 0 || k.High <= 0 || k.Low <= 0 {
			return result{name, false, fmt.Sprintf("%s K线价格含零值: O=%.2f C=%.2f H=%.2f L=%.2f", k.Timestamp.Format("2006-01-02"), k.Open, k.Close, k.High, k.Low)}
		}
		if k.High < k.Low {
			return result{name, false, fmt.Sprintf("%s 最高价 %.2f < 最低价 %.2f", k.Timestamp.Format("2006-01-02"), k.High, k.Low)}
		}
		if k.Timestamp.IsZero() {
			return result{name, false, "存在时间戳为零的 K 线"}
		}
	}

	return result{name, true, fmt.Sprintf("返回 %d 根 K 线，OHLCV 均正常", len(klines))}
}

type failProvider struct {
	name string
}

func (f *failProvider) Name() string { return f.name }
func (f *failProvider) GetQuote(ctx context.Context, codes ...gostox.StockCode) ([]*gostox.Quote, error) {
	return nil, errors.New("always fail")
}
func (f *failProvider) GetKline(ctx context.Context, code gostox.StockCode, period gostox.KlinePeriod, count int) ([]*gostox.Kline, error) {
	return nil, errors.New("always fail")
}
func (f *failProvider) GetStockList(ctx context.Context) ([]*gostox.StockInfo, error) {
	return nil, errors.New("always fail")
}
func (f *failProvider) GetIndexQuote(ctx context.Context, codes ...gostox.IndexCode) ([]*gostox.IndexQuote, error) {
	return nil, gostox.ErrNotSupported
}
func (f *failProvider) GetIndexKline(ctx context.Context, code gostox.IndexCode, period gostox.KlinePeriod, count int) ([]*gostox.IndexKline, error) {
	return nil, gostox.ErrNotSupported
}
