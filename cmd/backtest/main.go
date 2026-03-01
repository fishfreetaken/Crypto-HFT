package main

import (
	"fmt"
	"log"
	"os"

	"biance/tradelib"
)

// ===== 历史回测主程序 =====
//
// 注意：策略内部使用 time.Now() 处理冷却/超时/衰减等时间相关逻辑。
// 回测为即时重放（无等待），因此上述时间参数不会按真实时间推进，
// 仅交易触发的盈亏（止损/止盈/目标）能准确反映价格走势。

func main() {
	// 工作目录修正：如果在 cmd/backtest 目录下执行，则退回根目录
	if _, err := os.Stat("strategy/config.json"); os.IsNotExist(err) {
		if _, err := os.Stat("../../strategy/config.json"); err == nil {
			os.Chdir("../../")
		}
	}

	cfgPath := "strategy/config.json"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}

	appCfg, err := tradelib.LoadAppConfig(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("未找到 %s，使用默认配置\n", cfgPath)
		} else {
			log.Fatalf("配置文件解析失败: %v\n", err)
		}
	} else {
		log.Printf("已加载配置: %s (%d 个策略)\n", cfgPath, len(appCfg.Strategies))
	}

	// 加载全量历史价格记录
	records, err := tradelib.LoadAllPriceRecordsFromDir(appCfg.DataDir)
	if err != nil {
		log.Fatalf("加载历史数据失败: %v\n", err)
	}
	if len(records) == 0 {
		log.Fatalf("未找到历史数据（%s/），请先启动 collector\n", appCfg.DataDir)
	}

	startTime := records[0].Ts
	endTime := records[len(records)-1].Ts
	fakeEndTime := endTime.Add(0) // 传给 OnPrice 仅供状态显示，Quiet 模式下不输出

	// 开启静默模式（只显示交易开/平仓事件，不显示每tick状态行）
	strategies := make([]*tradelib.Strategy, len(appCfg.Strategies))
	for i, sc := range appCfg.Strategies {
		sc.Quiet = true
		strategies[i] = tradelib.NewStrategy(sc, nil)
	}

	// 启动信息
	totalCapital := 0.0
	for _, s := range strategies {
		if !s.Cfg.Disabled {
			totalCapital += s.Cfg.InitialCapital
		}
	}
	fmt.Println("========== BTC 多策略历史回测 ==========")
	fmt.Printf("数据目录: %s | 记录数: %d 条\n", appCfg.DataDir, len(records))
	fmt.Printf("时间范围: %s ~ %s\n",
		startTime.Format("2006-01-02 15:04:05"),
		endTime.Format("2006-01-02 15:04:05"))
	fmt.Printf("总资金: $%.2f | 策略数: %d\n", totalCapital, len(strategies))
	fmt.Println("注意: 冷却/超时/衰减等时间参数在即时回放中不准确")
	fmt.Println("=========================================")

	// 回测主循环：逐 tick 重放
	var lastTick tradelib.Tick
	total := len(records)
	for i, rec := range records {
		tick := tradelib.Tick{
			Prc:  rec.Price,
			Bid1: rec.Price - 0.1,
			Ask1: rec.Price + 0.1,
		}
		lastTick = tick
		for _, s := range strategies {
			s.OnPrice(tick, fakeEndTime)
		}
		if (i+1)%1000 == 0 || i+1 == total {
			fmt.Printf("[进度] %d/%d (%.1f%%) @ $%.2f  %s\n",
				i+1, total, float64(i+1)/float64(total)*100,
				rec.Price, rec.Ts.Format("2006-01-02 15:04"))
		}
	}

	// 回测结束：强制平仓 + 打印报告
	fmt.Println("\n──── 回测结束，强制平仓 ────")
	for _, s := range strategies {
		s.ForceLiquidate(lastTick, "回测结束")
	}

	tradelib.PrintAllReports(strategies, startTime, lastTick)
}
