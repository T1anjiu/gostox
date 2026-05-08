package main

import (
	"context"
	"fmt"
	"os"
	"time"

	gostox "github.com/T1anjiu/GoStox"
	"github.com/T1anjiu/GoStox/providers/eastmoney"
)

func main() {
	p := eastmoney.NewProvider()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 1. 实时行情
	fmt.Println("=== 1. GetQuote (sh600000, sz000001) ===")
	quotes, err := p.GetQuote(ctx,
		gostox.StockCode{Market: gostox.MarketSH, Code: "600000"},
		gostox.StockCode{Market: gostox.MarketSZ, Code: "000001"},
	)
	if err != nil {
		fmt.Printf("[FAIL] %v\n", err)
	} else {
		for _, q := range quotes {
			fmt.Printf("[PASS] %s %s 当前=%.2f 涨跌=%.2f(%.2f%%) 高=%.2f 低=%.2f 量=%d\n",
				q.Code, q.Name, q.Current, q.Change, q.ChangePct, q.High, q.Low, q.Volume)
		}
	}

	// 2. K线
	fmt.Println("\n=== 2. GetKline (sh600000, 日K, 5条) ===")
	klines, err := p.GetKline(ctx,
		gostox.StockCode{Market: gostox.MarketSH, Code: "600000"},
		gostox.KlinePeriodDay, 5,
	)
	if err != nil {
		fmt.Printf("[FAIL] %v\n", err)
	} else {
		for _, k := range klines {
			fmt.Printf("[PASS] %s O=%.2f C=%.2f H=%.2f L=%.2f V=%d\n",
				k.Timestamp.Format("2006-01-02"), k.Open, k.Close, k.High, k.Low, k.Volume)
		}
	}

	// 3. 股票列表
	fmt.Println("\n=== 3. GetStockList ===")
	list, err := p.GetStockList(ctx)
	if err != nil {
		fmt.Printf("[FAIL] %v\n", err)
	} else {
		fmt.Printf("[PASS] 共 %d 只股票\n", len(list))
		for i, s := range list {
			if i >= 3 {
				break
			}
			fmt.Printf("  %s %s\n", s.Code, s.Name)
		}
	}

	if err != nil || quotes == nil || klines == nil {
		os.Exit(1)
	}
}