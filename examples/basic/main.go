package main

import (
	"context"
	"fmt"
	"log"
	"time"

	gostox "github.com/T1anjiu/gostox"
	"github.com/T1anjiu/gostox/providers/eastmoney"
	"github.com/T1anjiu/gostox/providers/sina"
	"github.com/T1anjiu/gostox/providers/tencent"
)

func main() {
	em := eastmoney.NewProvider()
	sn := sina.NewProvider()
	tx := tencent.NewProvider()

	client, err := gostox.NewClient(em, sn, tx)
	if err != nil {
		log.Fatal(err)
	}
	client.SetOnProviderFail(func(name, method string, err error, _ time.Duration) {
		log.Printf("[warn] provider %s %s failed: %v", name, method, err)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	fmt.Println("=== 实时行情 ===")
	quotes, err := client.GetQuote(ctx,
		gostox.StockCode{Market: gostox.MarketSH, Code: "600000"},
		gostox.StockCode{Market: gostox.MarketSZ, Code: "000001"},
	)
	if err != nil {
		log.Fatal(err)
	}
	for _, q := range quotes {
		fmt.Printf("%s %s | 现:%.2f 涨跌:%+.2f (%+.2f%%) | 开:%.2f 高:%.2f 低:%.2f | 量:%d\n",
			q.Code, q.Name, q.Current, q.Change, q.ChangePct, q.Open, q.High, q.Low, q.Volume)
	}

	fmt.Println("\n=== 日K线 (浦发银行) ===")
	klines, err := client.GetKline(ctx,
		gostox.StockCode{Market: gostox.MarketSH, Code: "600000"},
		gostox.KlinePeriodDay,
		5,
	)
	if err != nil {
		log.Fatal(err)
	}
	for _, k := range klines {
		fmt.Printf("%s | 开:%.2f 收:%.2f 高:%.2f 低:%.2f | 量:%d\n",
			k.Timestamp.Format("2006-01-02"), k.Open, k.Close, k.High, k.Low, k.Volume)
	}

	fmt.Println("\n=== 沪深A股列表 (前10) ===")
	list, err := client.GetStockList(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for i, s := range list {
		if i >= 10 {
			break
		}
		fmt.Printf("%s %s\n", s.Code, s.Name)
	}
	fmt.Printf("... 共 %d 只股票\n", len(list))
}
