package tradelib

import (
	"fmt"
	"log"
	"time"
)

// ===== 策略运行时 =====

type Strategy struct {
	Cfg           Config       // 导出：外部可访问策略配置
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
	trhDual       dualPortfolio // trend_reversion_hedge：双腿仓位状态
	trapConsecDown int          // liq_trap：扫荡后连续下跌 tick 计数（确认上方扫荡回归）
	trapConsecUp   int          // liq_trap：扫荡后连续上涨 tick 计数（确认下方扫荡回归）

	// Stateful Indicators
	emaShort *StatefulZLEMA
	emaLong  *StatefulZLEMA
	emaTrend *StatefulEMA
	emaKC    *StatefulEMA
}

// NewStrategy 创建新策略实例，history 为历史价格（用于预热）
func NewStrategy(cfg Config, history []float64) *Strategy {
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
	if cfg.StrategyType == "trend_reversion_hedge" {
		warmup = cfg.TRHFastWindowTicks
		if cfg.TRHSlowWindowTicks > warmup {
			warmup = cfg.TRHSlowWindowTicks
		}
		if warmup == 0 {
			warmup = 10
		}
	}
	if cfg.StrategyType == "liq_trap" {
		warmup = cfg.LHTrapWindow + 20
		if warmup < 30 {
			warmup = 30
		}
	}
	// maxWindow estimation for ring buffer sizes
	maxWindow := warmup + 500
	if maxWindow < 1000 {
		maxWindow = 1000
	}
	s := &Strategy{
		Cfg:        cfg,
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

// FeedOnly 仅更新价格缓冲、EMA 和 Kalman 状态，不运行交易决策逻辑。
// 供选择器用于保持非激活策略的指标持续预热，切换时无需重新热身。
func (s *Strategy) FeedOnly(tick Tick) {
	s.feedPrice(tick.Prc)
	if s.Cfg.KalmanR > 0 {
		p2 := tick.Prc * tick.Prc
		s.kf.step(tick.Prc,
			p2*s.Cfg.KalmanQPos*s.Cfg.KalmanQPos,
			p2*s.Cfg.KalmanQVel*s.Cfg.KalmanQVel,
			p2*s.Cfg.KalmanR*s.Cfg.KalmanR)
	}
}

// InPosition 报告策略当前是否持有仓位
func (s *Strategy) InPosition() bool {
	if s.Cfg.StrategyType == "trend_reversion_hedge" {
		return s.trhDual.state == trhDualHold
	}
	return s.p.inPosition()
}

// CurrentEquity 返回策略当前总权益（含未实现盈亏）
func (s *Strategy) CurrentEquity(tick Tick) float64 {
	if s.Cfg.StrategyType == "trend_reversion_hedge" {
		return s.trhTotalEquity(tick)
	}
	return s.p.totalEquity(s.Cfg, tick)
}

// UpdateConfig 热重载时更新策略配置（structural 参数如 EMA 周期需重启生效）
func (s *Strategy) UpdateConfig(cfg Config) {
	s.Cfg = cfg
	s.warmupNeed = max(cfg.TrendPeriod, cfg.EMALong)
}

// SetCapital 注入更新后的总资金（由选择器在每次激活前调用，实现全量再投入）。
// 仅在未持仓时更新 portfolio 状态；trend_reversion_hedge 暂不支持。
func (s *Strategy) SetCapital(amount float64) {
	s.Cfg.InitialCapital = amount
	if s.Cfg.StrategyType != "trend_reversion_hedge" && !s.p.inPosition() {
		s.p.cash = amount
		s.p.peakEquity = amount
	}
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

// OnPrice 处理一次价格更新：根据策略类型分发到具体处理器。
// 禁用的策略直接返回（no-op）。
func (s *Strategy) OnPrice(tick Tick, endTime time.Time) {
	if s.Cfg.Disabled {
		return
	}
	switch s.Cfg.StrategyType {
	case "trend_prob":
		s.onPriceTrendProb(tick, endTime)
	case "squeeze_breakout":
		s.onPriceSqueezeBreakout(tick, endTime)
	case "dead_cat_bounce":
		s.onPriceDeadCatBounce(tick, endTime)
	case "waterfall":
		s.onPriceWaterfall(tick, endTime)
	case "trend_reversion_hedge":
		s.onPriceTRH(tick, endTime)
	case "liq_trap":
		s.onPriceLiqTrap(tick, endTime)
	default:
		s.onPriceEMA(tick, endTime)
	}
}

// ForceLiquidate 强制平仓（到期 / 中断退出时调用）
func (s *Strategy) ForceLiquidate(tick Tick, reason string) {
	if s.Cfg.Disabled {
		return
	}
	if s.Cfg.StrategyType == "trend_reversion_hedge" {
		dp := &s.trhDual
		if dp.state == trhDualHold {
			if dp.mode == trhModeFlashRevert {
				s.trhCloseBothA(tick, reason)
			} else {
				s.trhCloseBothB(tick, reason)
			}
		}
		return
	}
	if s.p.inPosition() {
		s.p.closePos(s.Cfg, tick, reason, &s.trades)
	}
}

func (s *Strategy) printStatus(tick Tick, endTime time.Time, indicators string, signal string) {
	if s.Cfg.Quiet && signal == "" {
		return
	}
	equity := s.p.totalEquity(s.Cfg, tick)
	pnl := equity - s.Cfg.InitialCapital
	pnlPct := pnl / s.Cfg.InitialCapital * 100
	remaining := time.Until(endTime).Round(time.Second)

	position := "空仓"
	if s.p.inPosition() {
		pct := s.p.positionPct(tick)
		position = fmt.Sprintf("%s仓 价格%+.3f%% 权益%+.2f%%",
			s.p.direction, pct*100, pct*s.Cfg.Leverage*100)
	}

	if signal != "" {
		fmt.Printf("[%s] [%-4s] %s | 权益:$%.2f(%+.2f%%) | %-30s | 剩余:%v | %s\n",
			time.Now().Format("15:04:05"), s.Cfg.Name, indicators, equity, pnlPct, position, remaining, signal)
	} else {
		fmt.Printf("[%s] [%-4s] %s | 权益:$%.2f(%+.2f%%) | %-30s | 剩余:%v\n",
			time.Now().Format("15:04:05"), s.Cfg.Name, indicators, equity, pnlPct, position, remaining)
	}
}

func (s *Strategy) printReport(startTime time.Time, lastTick Tick) {
	elapsed := time.Since(startTime).Round(time.Second)
	var finalEquity float64
	if s.Cfg.StrategyType == "trend_reversion_hedge" {
		finalEquity = s.trhTotalEquity(lastTick)
	} else {
		finalEquity = s.p.totalEquity(s.Cfg, lastTick)
	}
	pnl := finalEquity - s.Cfg.InitialCapital
	pnlPct := pnl / s.Cfg.InitialCapital * 100

	fmt.Printf("\n─── [%s] 策略报告 ───────────────────────────\n", s.Cfg.Name)
	fmt.Printf("运行: %v | 初始: $%.2f | 最终: $%.2f\n", elapsed, s.Cfg.InitialCapital, finalEquity)
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

// PrintAllReports 打印各策略详细报告 + 汇总对比表（跳过已禁用策略）
func PrintAllReports(strategies []*Strategy, startTime time.Time, lastTick Tick) {
	for _, s := range strategies {
		if s.Cfg.Disabled {
			continue
		}
		s.printReport(startTime, lastTick)
	}

	// 只汇总启用的策略
	var enabled []*Strategy
	for _, s := range strategies {
		if !s.Cfg.Disabled {
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
		var fin float64
		if s.Cfg.StrategyType == "trend_reversion_hedge" {
			fin = s.trhTotalEquity(lastTick)
		} else {
			fin = s.p.totalEquity(s.Cfg, lastTick)
		}
		pnl := fin - s.Cfg.InitialCapital
		pnlPct := pnl / s.Cfg.InitialCapital * 100
		totalInit += s.Cfg.InitialCapital
		totalFinal += fin
		color := "\033[32m"
		if pnl < 0 {
			color = "\033[31m"
		}
		pnlStr := fmt.Sprintf("%s%+.2f%%(%+.0f)\033[0m", color, pnlPct, pnl)
		fmt.Printf("║ %-8s ║ $%-7.2f ║ $%-7.2f ║ %-23s║\n",
			s.Cfg.Name, s.Cfg.InitialCapital, fin, pnlStr)
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
