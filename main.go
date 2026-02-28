package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// ===== 主程序 =====

func main() {
	cfgPath := "config.json"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	var err error
	appCfg, err = loadAppConfig(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("未找到 %s，使用默认配置\n", cfgPath)
		} else {
			log.Fatalf("配置文件解析失败: %v\n", err)
		}
	} else {
		log.Printf("已加载配置: %s (%d 个策略)\n", cfgPath, len(appCfg.Strategies))
	}

	// 配置热重载 goroutine
	reloadCh := make(chan AppConfig, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchConfig(ctx, cfgPath, reloadCh)

	// 退出信号监听
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	// 加载历史价格（所有策略共用同一份历史数据，各自维护独立副本）
	historyF := loadHistoricalPricesFromDir(appCfg.DataDir, appCfg.LookbackHours)
	history := convertHistoricalToTicks(historyF)
	if len(history) > 0 {
		log.Printf("历史数据：从 %s/ 加载 %d 条记录\n", appCfg.DataDir, len(history))
	} else {
		log.Printf("未找到历史数据（%s/），建议先启动 collector\n", appCfg.DataDir)
	}

	// 初始化各策略
	strategies := make([]*Strategy, len(appCfg.Strategies))
	for i, sc := range appCfg.Strategies {
		strategies[i] = newStrategy(sc, historyF)
	}

	startTime := time.Now()
	endTime := startTime.Add(appCfg.tradeDuration())
	var lastTick Tick
	if len(history) > 0 {
		lastTick = history[len(history)-1]
	}

	// 启动信息
	totalCapital := 0.0
	for _, s := range strategies {
		if !s.cfg.Disabled {
			totalCapital += s.cfg.InitialCapital
		}
	}
	fmt.Println("========== BTC 多策略模拟交易 ==========")
	fmt.Printf("交易对: %s | 总资金: $%.2f | 策略数: %d\n",
		appCfg.Symbol, totalCapital, len(strategies))
	for _, s := range strategies {
		disabledTag := ""
		if s.cfg.Disabled {
			disabledTag = " \033[90m[禁用]\033[0m"
		}
		fmt.Printf("  [%-4s] 资金:$%.2f 杠杆:%.0fx 止损:%.1f%% 止盈:%.1f%% EMA(%d/%d/%d)%s\n",
			s.cfg.Name, s.cfg.InitialCapital, s.cfg.Leverage,
			s.cfg.StopLoss*100, s.cfg.TakeProfit*100,
			s.cfg.EMAShort, s.cfg.EMALong, s.cfg.TrendPeriod, disabledTag)
	}
	fmt.Printf("运行时长: %v | 采样间隔: %v\n", appCfg.tradeDuration(), appCfg.sampleInterval())
	fmt.Println("=========================================")

	ticker := time.NewTicker(appCfg.sampleInterval())
	defer ticker.Stop()
	timer := time.NewTimer(appCfg.tradeDuration())
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			tick, err := fetchPrice()
			if err != nil {
				tick = lastTick
				log.Printf("到期平仓获取价格失败，使用最后已知价格 %v\n", tick)
			} else {
				lastTick = tick
			}
			for _, s := range strategies {
				s.forceLiquidate(tick, "到期")
			}
			printAllReports(strategies, startTime, lastTick)
			return

		case sv := <-sig:
			fmt.Printf("\n[%s] 收到退出信号 (%v)，正在结算...\n",
				time.Now().Format("15:04:05"), sv)
			for _, s := range strategies {
				s.forceLiquidate(lastTick, "中断退出")
			}
			printAllReports(strategies, startTime, lastTick)
			return

		case newCfg := <-reloadCh:
			// 按名称匹配更新策略运行时参数（structural 参数需重启生效）
			appCfg = newCfg
			for _, st := range strategies {
				for _, sc := range newCfg.Strategies {
					if sc.Name == st.cfg.Name {
						st.cfg = sc
						st.warmupNeed = max(sc.TrendPeriod, sc.EMALong)
						break
					}
				}
			}
			fmt.Printf("[%s] \033[36m配置热重载\033[0m (%d 个策略)\n",
				time.Now().Format("15:04:05"), len(newCfg.Strategies))

		case <-ticker.C:
			tick, err := fetchPrice()
			if err != nil {
				log.Printf("获取价格失败: %v\n", err)
				continue
			}
			lastTick = tick
			fmt.Printf("[%s] ── BTC $%.2f (B:%.2f A:%.2f) ────────────────\n",
				time.Now().Format("15:04:05"), tick.Prc, tick.Bid1, tick.Ask1)
			for _, s := range strategies {
				s.onPrice(tick, endTime)
			}
		}
	}
}
