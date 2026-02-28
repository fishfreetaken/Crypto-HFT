package main

import (
	"fmt"
	"log"
	"time"
)

// ===== 策略运行时 =====

type Strategy struct {
	cfg           Config
	p             portfolio
	priceBuf      *RingBuffer
	trades        []tradeRecord
	prevShortEMA  float64
	prevLongEMA   float64
	safetyUntil   time.Time
	warmupNeed    int
	currentTarget float64     // trend_prob：本次开仓的初始目标权益收益率
	openTime      time.Time   // trend_prob / squeeze_breakout：开仓时间
	kf            kalmanFilter // 卡尔曼滤波器状态（每策略独立，跨 tick 持久化）
	prevInSqueeze bool         // squeeze_breakout：上一 tick 是否处于挤压状态
	dcbConsecDown int          // dead_cat_bounce：反弹高点后连续下跌 tick 计数

	// Stateful Indicators
	emaShort *StatefulZLEMA
	emaLong  *StatefulZLEMA
	emaTrend *StatefulEMA
	emaKC    *StatefulEMA
}

func newStrategy(cfg Config, history []float64) *Strategy {
	warmup := max(cfg.TrendPeriod, cfg.EMALong)
	if cfg.BBPeriod > warmup {
		warmup = cfg.BBPeriod
	}
	if cfg.StrategyType == "trend_prob" {
		warmup = cfg.TrendLookback + 1
	}
	if cfg.StrategyType == "squeeze_breakout" {
		warmup = max(cfg.SqueezeBBPeriod, cfg.SqueezeATRPeriod) + 1
	}
	if cfg.StrategyType == "dead_cat_bounce" {
		warmup = cfg.DCBDropPeriod
	}
	if cfg.StrategyType == "waterfall" {
		warmup = cfg.WFConsecutiveTicks + 10
	}
	// maxWindow estimation for ring buffer sizes
	maxWindow := warmup + 500
	if maxWindow < 1000 {
		maxWindow = 1000
	}
	s := &Strategy{
		cfg:        cfg,
		p:          portfolio{name: cfg.Name, cash: cfg.InitialCapital, peakEquity: cfg.InitialCapital},
		warmupNeed: warmup,
		priceBuf:   NewRingBuffer(maxWindow),
	}

	// Initialize required EMAs
	if cfg.EMAShort > 0 {
		s.emaShort = NewStatefulZLEMA(cfg.EMAShort)
	}
	if cfg.EMALong > 0 {
		s.emaLong = NewStatefulZLEMA(cfg.EMALong)
	}
	if cfg.TrendPeriod > 0 {
		s.emaTrend = NewStatefulEMA(cfg.TrendPeriod)
	}
	if cfg.SqueezeATRPeriod > 0 {
		s.emaKC = NewStatefulEMA(cfg.SqueezeATRPeriod)
	}

	if len(history) > 0 {
		for _, p := range history {
			s.feedPrice(p)
		}
		if s.priceBuf.count >= s.warmupNeed {
			if s.emaShort != nil {
				s.prevShortEMA = s.emaShort.Value()
			}
			if s.emaLong != nil {
				s.prevLongEMA = s.emaLong.Value()
			}
			log.Printf("[%-4s] 历史预热完成：%d 条记录\n", cfg.Name, s.priceBuf.count)
		} else {
			log.Printf("[%-4s] 历史数据不足（%d/%d），仍需预热\n", cfg.Name, s.priceBuf.count, s.warmupNeed)
		}
	}
	return s
}

// feedPrice updates internal buffer and global stateful indicators
func (s *Strategy) feedPrice(price float64) {
	s.priceBuf.Add(price)
	if s.emaShort != nil {
		s.emaShort.Update(price)
	}
	if s.emaLong != nil {
		s.emaLong.Update(price)
	}
	if s.emaTrend != nil {
		s.emaTrend.Update(price)
	}
	if s.emaKC != nil {
		s.emaKC.Update(price)
	}
}

// onPrice 处理一次价格更新：dispatches to strategy-specific handler.
// Returns immediately (no-op) if strategy is disabled.
func (s *Strategy) onPrice(tick Tick, endTime time.Time) {
	if s.cfg.Disabled {
		return
	}
	switch s.cfg.StrategyType {
	case "trend_prob":
		s.onPriceTrendProb(tick, endTime)
	case "squeeze_breakout":
		s.onPriceSqueezeBreakout(tick, endTime)
	case "dead_cat_bounce":
		s.onPriceDeadCatBounce(tick, endTime)
	case "waterfall":
		s.onPriceWaterfall(tick, endTime)
	default:
		s.onPriceEMA(tick, endTime)
	}
}

// forceLiquidate 强制平仓（到期 / 中断退出时调用）
func (s *Strategy) forceLiquidate(tick Tick, reason string) {
	if s.cfg.Disabled {
		return
	}
	if s.p.inPosition() {
		s.p.closePos(s.cfg, tick, reason, &s.trades)
	}
}

func (s *Strategy) printStatus(tick Tick, endTime time.Time, indicators string, signal string) {
	equity := s.p.totalEquity(s.cfg, tick)
	pnl := equity - s.cfg.InitialCapital
	pnlPct := pnl / s.cfg.InitialCapital * 100
	remaining := time.Until(endTime).Round(time.Second)

	position := "空仓"
	if s.p.inPosition() {
		pct := s.p.positionPct(tick)
		position = fmt.Sprintf("%s仓 价格%+.3f%% 权益%+.2f%%",
			s.p.direction, pct*100, pct*s.cfg.Leverage*100)
	}

	if signal != "" {
		fmt.Printf("[%s] [%-4s] %s | 权益:$%.2f(%+.2f%%) | %-30s | 剩余:%v | %s\n",
			time.Now().Format("15:04:05"), s.cfg.Name, indicators, equity, pnlPct, position, remaining, signal)
	} else {
		fmt.Printf("[%s] [%-4s] %s | 权益:$%.2f(%+.2f%%) | %-30s | 剩余:%v\n",
			time.Now().Format("15:04:05"), s.cfg.Name, indicators, equity, pnlPct, position, remaining)
	}
}

func (s *Strategy) printReport(startTime time.Time, lastTick Tick) {
	elapsed := time.Since(startTime).Round(time.Second)
	finalEquity := s.p.totalEquity(s.cfg, lastTick)
	pnl := finalEquity - s.cfg.InitialCapital
	pnlPct := pnl / s.cfg.InitialCapital * 100

	fmt.Printf("\n─── [%s] 策略报告 ───────────────────────────\n", s.cfg.Name)
	fmt.Printf("运行: %v | 初始: $%.2f | 最终: $%.2f\n", elapsed, s.cfg.InitialCapital, finalEquity)
	if pnl >= 0 {
		fmt.Printf("盈亏: \033[32m+$%.2f (+%.2f%%)\033[0m\n", pnl, pnlPct)
	} else {
		fmt.Printf("盈亏: \033[31m-$%.2f (%.2f%%)\033[0m\n", -pnl, pnlPct)
	}
	fmt.Printf("做多:%d  做空:%d  平仓:%d\n", s.p.longCount, s.p.shortCount, s.p.closeCount)
	for i, t := range s.trades {
		fmt.Printf("  %2d. [%s] %-14s @ $%.2f | 权益:$%.2f\n",
			i+1, t.ts.Format("15:04:05"), t.action, t.price, t.equity)
	}
	if len(s.trades) == 0 {
		fmt.Println("  本次未触发任何交易信号。")
	}
}

// printAllReports 打印各策略详细报告 + 汇总对比表（跳过已禁用策略）
func printAllReports(strategies []*Strategy, startTime time.Time, lastTick Tick) {
	for _, s := range strategies {
		if s.cfg.Disabled {
			continue
		}
		s.printReport(startTime, lastTick)
	}

	// 只汇总启用的策略
	var enabled []*Strategy
	for _, s := range strategies {
		if !s.cfg.Disabled {
			enabled = append(enabled, s)
		}
	}
	if len(enabled) == 0 {
		return
	}

	fmt.Println("\n╔══════════════════════════════════════════════════╗")
	fmt.Println("║                   汇总对比报告                  ║")
	fmt.Println("╠══════════╦══════════╦══════════╦════════════════╣")
	fmt.Printf("║ %-8s ║ %-8s ║ %-8s ║ %-14s ║\n", "策略", "初始资金", "最终权益", "总盈亏")
	fmt.Println("╠══════════╬══════════╬══════════╬════════════════╣")

	totalInit, totalFinal := 0.0, 0.0
	for _, s := range enabled {
		fin := s.p.totalEquity(s.cfg, lastTick)
		pnl := fin - s.cfg.InitialCapital
		pnlPct := pnl / s.cfg.InitialCapital * 100
		totalInit += s.cfg.InitialCapital
		totalFinal += fin
		color := "\033[32m"
		if pnl < 0 {
			color = "\033[31m"
		}
		pnlStr := fmt.Sprintf("%s%+.2f%%(%+.0f)\033[0m", color, pnlPct, pnl)
		fmt.Printf("║ %-8s ║ $%-7.2f ║ $%-7.2f ║ %-23s║\n",
			s.cfg.Name, s.cfg.InitialCapital, fin, pnlStr)
	}

	fmt.Println("╠══════════╬══════════╬══════════╬════════════════╣")
	totalPnl := totalFinal - totalInit
	totalPnlPct := totalPnl / totalInit * 100
	totalColor := "\033[32m"
	if totalPnl < 0 {
		totalColor = "\033[31m"
	}
	totalStr := fmt.Sprintf("%s%+.2f%%(%+.0f)\033[0m", totalColor, totalPnlPct, totalPnl)
	fmt.Printf("║ %-8s ║ $%-7.2f ║ $%-7.2f ║ %-23s║\n",
		"总计", totalInit, totalFinal, totalStr)
	fmt.Println("╚══════════╩══════════╩══════════╩════════════════╝")
}
