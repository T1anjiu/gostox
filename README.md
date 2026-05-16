# gostox

`gostox` 是一个面向 A 股市场的 Go 行情库，提供统一的实时行情、K 线和股票列表接口，并支持在多个数据源之间按顺序故障切换。

当前内置了 4 个数据源实现：

- `eastmoney`
- `sina`
- `tencent`
- `tushare`（需注册获取 token）

这个库的目标是让 Go 项目能用一致的数据模型接入常见 A 股行情源，而不是分别处理每个 provider 的返回格式。

## 特性

- 统一的 `Quote`、`Kline`、`StockInfo`、`IndexQuote`、`IndexKline` 数据模型
- 支持多 provider 顺序降级
- 所有接口都支持 `context.Context`
- 支持批量实时行情查询（含指数）
- 支持分钟级和日周月 K 线（含指数 K 线）
- 支持东方财富 A 股股票列表
- 对部分解析失败返回 `PartialError`，保留已成功解析的数据

## 当前支持范围

当前版本的支持范围比较明确：

- 市场：沪深 A 股，指数（上证、深证、创业板等）
- 数据类型：实时快照、K 线、指数行情、指数 K 线、股票列表
- provider：东方财富、新浪、腾讯、Tushare Pro

### Provider 能力对比

| Provider | GetQuote | GetKline | GetStockList | GetIndexQuote | GetIndexKline |
| --- | --- | --- | --- | --- | --- |
| `eastmoney` | Yes | Yes | Yes | Yes | Yes |
| `sina` | Yes | Yes | No | No | No |
| `tencent` | Yes | Yes | No | No | No |
| `tushare` | Yes | Yes | Yes | Yes | Yes |

### K 线周期支持

统一周期定义在 `gostox.KlinePeriod`：

- `KlinePeriod1Min`
- `KlinePeriod5Min`
- `KlinePeriod15Min`
- `KlinePeriod30Min`
- `KlinePeriod60Min`
- `KlinePeriodDay`
- `KlinePeriodWeek`
- `KlinePeriodMonth`

说明：

- `sina` 当前不支持 `KlinePeriod1Min`
- `eastmoney`、`tencent` 和 `tushare` 支持上述全部周期

## 不支持的范围

当前代码没有提供这些能力：

- 港股、美股、期货、期权、外汇、加密货币
- 逐笔成交、盘口深度、Level-2
- WebSocket 推送行情
- 财务数据、公告、行业板块
- 交易下单能力

## 安装

```bash
go get github.com/T1anjiu/gostox
```

要求：

- Go `1.25+`

## 快速开始

### 1. 创建单个 provider

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	gostox "github.com/T1anjiu/gostox"
	"github.com/T1anjiu/gostox/providers/eastmoney"
)

func main() {
	p := eastmoney.NewProvider()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	quotes, err := p.GetQuote(ctx,
		gostox.StockCode{Market: gostox.MarketSH, Code: "600000"},
		gostox.StockCode{Market: gostox.MarketSZ, Code: "000001"},
	)
	if err != nil {
		log.Fatal(err)
	}

	for _, q := range quotes {
		fmt.Printf("%s %s %.2f\n", q.Code, q.Name, q.Current)
	}
}
```

### 2. 使用多 provider 故障切换

`Client` 会按你传入的顺序依次尝试 provider。

```go
package main

import (
	"context"
	"log"
	"time"

	gostox "github.com/T1anjiu/gostox"
	"github.com/T1anjiu/gostox/providers/eastmoney"
	"github.com/T1anjiu/gostox/providers/sina"
	"github.com/T1anjiu/gostox/providers/tencent"
)

func main() {
	client, err := gostox.NewClient(
		eastmoney.NewProvider(),
		sina.NewProvider(),
		tencent.NewProvider(),
	)
	if err != nil {
		log.Fatal(err)
	}

	client.SetOnProviderFail(func(name, method string, err error, latency time.Duration) {
		log.Printf("provider %s %s failed in %s: %v", name, method, latency, err)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if _, err := client.GetQuote(ctx, gostox.StockCode{Market: gostox.MarketSH, Code: "600000"}); err != nil {
		log.Fatal(err)
	}
}
```

### 3. 获取实时行情

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()

quotes, err := client.GetQuote(ctx,
	gostox.StockCode{Market: gostox.MarketSH, Code: "600000"},
	gostox.StockCode{Market: gostox.MarketSZ, Code: "000001"},
)
if err != nil {
	log.Fatal(err)
}

for _, q := range quotes {
	fmt.Printf("%s %s | 现:%.2f 涨跌:%+.2f (%+.2f%%)\n",
		q.Code, q.Name, q.Current, q.Change, q.ChangePct)
}
```

### 4. 获取 K 线

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()

klines, err := client.GetKline(ctx,
	gostox.StockCode{Market: gostox.MarketSH, Code: "600000"},
	gostox.KlinePeriodDay,
	5,
)
if err != nil {
	log.Fatal(err)
}

for _, k := range klines {
	fmt.Printf("%s O=%.2f C=%.2f H=%.2f L=%.2f V=%d\n",
		k.Timestamp.Format("2006-01-02"),
		k.Open, k.Close, k.High, k.Low, k.Volume,
	)
}
```

### 5. 获取股票列表

只有 `eastmoney` 当前支持 `GetStockList`。

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

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
```

### 6. 获取指数行情

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()

quotes, err := client.GetIndexQuote(ctx,
	gostox.IndexCode{Code: "000001"}, // 上证指数
	gostox.IndexCode{Code: "399001"}, // 深证成指
	gostox.IndexCode{Code: "399006"}, // 创业板指
)
if err != nil {
	log.Fatal(err)
}

for _, q := range quotes {
	fmt.Printf("%s %s | 现:%.2f 涨跌:%+.2f (%+.2f%%)\n",
		q.Code, q.Name, q.Current, q.Change, q.ChangePct)
}
```

### 7. 获取指数 K 线

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()

klines, err := client.GetIndexKline(ctx,
	gostox.IndexCode{Code: "000001"},
	gostox.KlinePeriodDay,
	5,
)
if err != nil {
	log.Fatal(err)
}

for _, k := range klines {
	fmt.Printf("%s O=%.2f C=%.2f H=%.2f L=%.2f V=%d\n",
		k.Timestamp.Format("2006-01-02"),
		k.Open, k.Close, k.High, k.Low, k.Volume,
	)
}
```

## IndexCode 使用方式

```go
ic := gostox.IndexCode{Code: "000001"}
fmt.Println(ic.String())             // 000001
fmt.Println(ic.EastmoneyIndexCode()) // 1.000001
```

常用指数代码：

| 代码 | 名称 |
| --- | --- |
| 000001 | 上证指数 |
| 000016 | 上证50 |
| 000300 | 沪深300 |
| 000688 | 科创50 |
| 000905 | 中证500 |
| 399001 | 深证成指 |
| 399006 | 创业板指 |

## StockCode 使用方式

`gostox` 使用 `StockCode` 统一表示 A 股代码：

```go
code := gostox.StockCode{Market: gostox.MarketSH, Code: "600000"}
fmt.Println(code.String())        // sh600000
fmt.Println(code.SinaCode())      // sh600000
fmt.Println(code.TencentCode())   // sh600000
fmt.Println(code.EastmoneyCode()) // 1.600000
```

也可以从字符串解析：

```go
code, err := gostox.ParseStockCode("sz000001")
if err != nil {
	log.Fatal(err)
}
```

## 错误处理

### `ErrNotSupported`

某个 provider 不支持特定方法时，返回 `gostox.ErrNotSupported`。

在 `Client` 中，这类错误会自动跳过并尝试下一个 provider。

### `PartialError`

批量请求中如果有部分记录解析成功、部分失败，会返回：

- 成功解析的数据
- `*gostox.PartialError`

示例：

```go
quotes, err := client.GetQuote(ctx,
	gostox.StockCode{Market: gostox.MarketSH, Code: "600000"},
	gostox.StockCode{Market: gostox.MarketSZ, Code: "000001"},
)

if err != nil {
	var pe *gostox.PartialError
	if errors.As(err, &pe) {
		log.Printf("partial success: %d failures", len(pe.Failures))
	} else {
		log.Fatal(err)
	}
}

for _, q := range quotes {
	_ = q
}
```

`Client` 对 `PartialError` 的行为是：

- 直接返回当前 provider 已成功的数据和错误
- 不会继续切换到下一个 provider

这是为了避免在部分成功场景下丢失已经取到的数据。

## 缓存装饰器

`providers/cache` 提供了带 TTL 的 `Provider` 包装器，可以叠加在任意 provider 上，让重复请求命中本地缓存而不打到上游。

```go
package main

import (
	"context"
	"log"
	"time"

	gostox "github.com/T1anjiu/gostox"
	"github.com/T1anjiu/gostox/providers/cache"
	"github.com/T1anjiu/gostox/providers/eastmoney"
)

func main() {
	cached := cache.New(eastmoney.NewProvider(), cache.DefaultTTL)

	client, err := gostox.NewClient(cached)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 第一次走网络，第二次命中缓存
	_, _ = client.GetQuote(ctx, gostox.StockCode{Market: gostox.MarketSH, Code: "600000"})
	_, _ = client.GetQuote(ctx, gostox.StockCode{Market: gostox.MarketSH, Code: "600000"})
}
```

`cache.DefaultTTL` 默认值：

| 方法 | TTL |
| --- | --- |
| `GetQuote` | 3s |
| `GetKline` | 60s |
| `GetStockList` | 24h |
| `GetIndexQuote` | 3s |
| `GetIndexKline` | 60s |

需要自定义可以直接构造 `cache.TTLConfig`：

```go
cached := cache.New(eastmoney.NewProvider(), cache.TTLConfig{
	Quote:      time.Second,
	Kline:      5 * time.Minute,
	StockList:  6 * time.Hour,
	IndexQuote: time.Second,
	IndexKline: 5 * time.Minute,
})
```

注意事项：

- `GetQuote` 和 `GetIndexQuote` 按代码分别缓存，相同代码命中、缺失代码会回源
- `GetKline` / `GetIndexKline` 的缓存键包含 `code + period + count`，参数变化会重新请求
- 当前缓存层没有 singleflight 合并，cache miss 时并发请求会同时打到上游
- 若上游返回 `PartialError`，已成功的数据仍会写入缓存，错误本身不缓存

## Provider 配置

每个 provider 都支持注入自定义 `http.Client`。

示例：

```go
httpClient := &http.Client{Timeout: 5 * time.Second}
provider := eastmoney.NewProvider(eastmoney.WithHTTPClient(httpClient))
```

`eastmoney` 还支持自定义 `ut` token：

```go
provider := eastmoney.NewProvider(
	eastmoney.WithUTToken("your-token"),
)
```

## 示例程序

项目内置了 3 个示例：

- `examples/basic`：多 provider 的基础使用方式
- `examples/eastmoney`：只使用东方财富 provider
- `examples/healthcheck`：真实网络调用健康检查

运行方式：

```bash
go run ./examples/basic
go run ./examples/eastmoney
go run ./examples/healthcheck
```

## 生产使用建议

当前项目更适合这些场景：

- 个人项目
- 内部工具
- 数据研究
- 行情展示
- 非核心、可降级的生产辅助链路

不建议直接作为这些场景的唯一核心依赖：

- 交易执行
- 清算结算
- 高可用核心行情基础设施

原因很简单：当前 provider 依赖的是网页接口，而不是稳定的官方商业行情接口。上游返回格式、限流策略和可用性都可能变化。

如果要用于生产，建议至少自行补充：

- 监控和告警
- provider 健康检查
- 本地缓存
- 限流和熔断
- 异常回归测试
- 长时间 shadow run 验证

## 免责声明

本项目使用的是公开网页接口的非官方封装，仅适合作为学习、研究、非关键业务或可降级场景的数据接入方案。

请不要把它直接视为交易级、结算级或 SLA 受保障的数据源。

使用本项目所产生的风险由使用者自行承担。

## 开发与测试

运行全部测试：

```bash
go test ./...
```

运行健康检查：

```bash
go run ./examples/healthcheck
```

## License

MIT
