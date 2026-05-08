// healthcheck 对三个 provider 逐一做真实网络调用，验证返回数据的合理性。
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

// 用浦发银行（sh600000）和平安银行（sz000001）做测试标的，两只都是流动性极好的股票。
var testCodes = []gostox.StockCode{
	{Market: gostox.MarketSH, Code: "600000"},
	{Market: gostox.MarketSZ, Code: "000001"},
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
	for _, p := range providers {
		results = append(results, checkQuote(p))
		results = append(results, checkKline(p))
	}
	// GetStockList 只有 eastmoney 支持，复用已创建的实例
	results = append(results, checkStockList(providers[0]))

	// 汇总输出
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
		// PartialError 不算完全失败，数据仍然返回了
		var pe *gostox.PartialError
		if !errors.As(err, &pe) {
			return result{name, false, fmt.Sprintf("请求失败: %v", err)}
		}
	}

	if len(quotes) == 0 {
		return result{name, false, "返回数据为空"}
	}

	// 验证每条数据的合理性
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
	}

	return result{name, true, fmt.Sprintf("返回 %d 条，价格/量/名称均正常", len(quotes))}
}

func checkKline(p gostox.Provider) result {
	name := fmt.Sprintf("%s/GetKline", p.Name())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	code := testCodes[0] // sh600000
	klines, err := p.GetKline(ctx, code, gostox.KlinePeriodDay, 10)
	if err != nil {
		var pe *gostox.PartialError
		if errors.Is(err, gostox.ErrNotSupported) {
			return result{name, true, "不支持（跳过）"}
		}
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

	// A 股数量应在合理范围内（目前约 5300 只）
	if len(list) < 4000 {
		return result{name, false, fmt.Sprintf("股票数量 %d 偏少（预期 > 4000），接口可能异常", len(list))}
	}

	// 抽查前几条数据格式
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
