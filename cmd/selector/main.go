// cmd/selector/main.go
//
// 动态策略选择器（博弈论 + 多臂老虎机框架）
//
// ══ 核心思路 ══════════════════════════════════════════════════════════════
//
//  综合分 = 市场适配分×wm + 历史胜率权重×wh + 随机扰动×wn
//
//  • 市场适配分：特征就绪时按行情打分（0~1），未就绪时以 0.50 中性代替，
//    保证选择器从第一个 tick 起就能激活策略，不等待数据积累。
//  • 历史胜率权重：盈利的策略权重上升，亏损的权重下降，形成正向反馈。
//  • 随机扰动：防止陷入局部最优，持续探索低权重策略。
//
// ══ 资金模型 ══════════════════════════════════════════════════════════════
//
//  全局共享一笔资金（total_capital），激活策略前通过 SetCapital 注入，
//  平仓后总资金 = 策略当前权益（全量再投入，盈亏实时复利）。
//
// ══ 价格数据 ══════════════════════════════════════════════════════════════
//
//  每 tick 优先从 collector 数据目录（data_dir）读取最近 2 小时历史，
//  若数据不足则回退到内存滚动缓冲（最多 300 条），两者均可用于特征计算。
//  历史数据充足时，第一个 tick 即可完成特征计算并开始按评分选择策略。

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"os/signal"
	"syscall"
	"time"

	"biance/tradelib"
)

// ══ 选择器独立配置 ════════════════════════════════════════════════════════

type SelectorConfig struct {
	Symbol            string            `json:"symbol"`
	SampleIntervalSec int               `json:"sample_interval_sec"`
	TradeDurationMin  int               `json:"trade_duration_min"`
	DataDir           string            `json:"data_dir"`
	LookbackHours     int               `json:"lookback_hours"`

	TotalCapital  float64 `json:"total_capital"`  // 共享总资金（默认 1000）
	WeightMarket  float64 `json:"weight_market"`  // 市场适配分权重（默认 0.65）
	WeightHistory float64 `json:"weight_history"` // 历史胜率权重（默认 0.20）
	WeightNoise   float64 `json:"weight_noise"`   // 随机扰动权重（默认 0.15）
	MaxPatience   int     `json:"max_patience"`   // 最大等待格数后重选（默认 20）

	Strategies []tradelib.Config `json:"strategies"`
}

func (c *SelectorConfig) SampleInterval() time.Duration {
	return time.Duration(c.SampleIntervalSec) * time.Second
}

func (c *SelectorConfig) TradeDuration() time.Duration {
	return time.Duration(c.TradeDurationMin) * time.Minute
}

func loadSelectorConfig(selectorPath, fallbackPath string) (SelectorConfig, string) {
	const (
		defCap  = 1000.0
		defWm   = 0.65
		defWh   = 0.20
		defWn   = 0.15
		defMaxP = 20
	)
	if data, err := os.ReadFile(selectorPath); err == nil {
		var cfg SelectorConfig
		if jsonErr := json.Unmarshal(data, &cfg); jsonErr == nil {
			if cfg.TotalCapital <= 0 {
				cfg.TotalCapital = defCap
			}
			if cfg.WeightMarket == 0 && cfg.WeightHistory == 0 && cfg.WeightNoise == 0 {
				cfg.WeightMarket, cfg.WeightHistory, cfg.WeightNoise = defWm, defWh, defWn
			}
			if cfg.MaxPatience <= 0 {
				cfg.MaxPatience = defMaxP
			}
			normalizeWeights(&cfg)
			log.Printf("已加载选择器配置: %s (%d 个策略)\n", selectorPath, len(cfg.Strategies))
			return cfg, selectorPath
		} else {
			log.Printf("selector.json 解析失败: %v，尝试 %s\n", jsonErr, fallbackPath)
		}
	}
	appCfg, err := tradelib.LoadAppConfig(fallbackPath)
	loadedFrom := fallbackPath
	if err != nil {
		log.Printf("未找到 %s，使用内置默认配置\n", fallbackPath)
		loadedFrom = "(内置默认)"
	} else {
		log.Printf("已加载基础配置: %s (%d 个策略)，选择器参数使用默认值\n",
			fallbackPath, len(appCfg.Strategies))
	}
	return SelectorConfig{
		Symbol: appCfg.Symbol, SampleIntervalSec: appCfg.SampleIntervalSec,
		TradeDurationMin: appCfg.TradeDurationMin, DataDir: appCfg.DataDir,
		LookbackHours: appCfg.LookbackHours,
		TotalCapital: defCap, WeightMarket: defWm, WeightHistory: defWh,
		WeightNoise: defWn, MaxPatience: defMaxP, Strategies: appCfg.Strategies,
	}, loadedFrom
}

func normalizeWeights(cfg *SelectorConfig) {
	total := cfg.WeightMarket + cfg.WeightHistory + cfg.WeightNoise
	if total > 0 && math.Abs(total-1.0) > 0.001 {
		cfg.WeightMarket /= total
		cfg.WeightHistory /= total
		cfg.WeightNoise /= total
	}
}

// ══ 市场特征 ══════════════════════════════════════════════════════════════

type MarketFeatures struct {
	Volatility float64
	ER         float64
	Trend      float64
	MaxDrop    float64
	AvgVel     float64
	BBWidth    float64
}

// ══ 选择器状态 ════════════════════════════════════════════════════════════

// scoreResult 单策略评分（market=-1 表示特征未就绪，用 0.50 中性代入计算）
type scoreResult struct {
	market float64 // 市场适配分，[0,1] 或 -1（未就绪）
	hist   float64 // 历史权重归一化 [0,1]
	base   float64 // 确定性总分 = eff_market×wm + hist×wh（不含噪声）
}

type StratSelector struct {
	cfg         *SelectorConfig
	strategies  []*tradelib.Strategy
	histWeights []float64

	activeIdx    int
	prevInPos    bool
	openEq       float64
	patienceTick int

	totalCapital   float64
	initialCapital float64

	prices     []float64      // 内存价格缓冲（collector数据不足时的回退）
	lastFeats  MarketFeatures // 最近一次特征
	featsReady bool           // 特征是否有效（为 false 时用 0.50 中性代替）
}

func newSelector(cfg *SelectorConfig, strategies []*tradelib.Strategy) *StratSelector {
	w := make([]float64, len(strategies))
	for i := range w {
		w[i] = 1.0
	}
	return &StratSelector{
		cfg: cfg, strategies: strategies, histWeights: w,
		activeIdx: -1, totalCapital: cfg.TotalCapital, initialCapital: cfg.TotalCapital,
	}
}

// ══ 价格缓冲 ══════════════════════════════════════════════════════════════

func (sel *StratSelector) pushPrice(price float64) {
	sel.prices = append(sel.prices, price)
	if len(sel.prices) > 300 {
		sel.prices = sel.prices[1:]
	}
}

// ══ 特征计算 ══════════════════════════════════════════════════════════════

// refreshFeatures 每 tick 调用。优先从 collector 数据目录读取最近 2 小时价格；
// 若不足 35 条则回退到内存缓冲。两者都不足时保持 featsReady=false，
// 但选择器仍会继续运行（使用 0.50 中性市场分）。
func (sel *StratSelector) refreshFeatures(currentPrice float64) {
	sel.pushPrice(currentPrice)

	// 优先 collector 数据
	prices := tradelib.LoadHistoricalPricesFromDir(sel.cfg.DataDir, 2)
	prices = append(prices, currentPrice)

	// 不足时回退到内存缓冲
	if len(prices) < 35 {
		prices = sel.prices
	}

	n := len(prices)
	if n < 35 {
		sel.featsReady = false
		return
	}

	const vw = 20
	var rSum, rSumSq float64
	for i := n - vw; i < n-1; i++ {
		if prices[i] > 0 {
			r := (prices[i+1] - prices[i]) / prices[i]
			rSum += r
			rSumSq += r * r
		}
	}
	rMean := rSum / float64(vw-1)
	vol := math.Sqrt(math.Max(0, rSumSq/float64(vw-1)-rMean*rMean))

	const ew = 30
	netMove := math.Abs(prices[n-1] - prices[n-ew])
	totalPath := 0.0
	for i := n - ew; i < n-1; i++ {
		totalPath += math.Abs(prices[i+1] - prices[i])
	}
	er := 0.5
	if totalPath > 0 {
		er = netMove / totalPath
	}

	trend := 0.0
	if prices[n-ew] > 0 {
		trend = (prices[n-1] - prices[n-ew]) / prices[n-ew]
	}

	const dw = 60
	dStart := n - dw
	if dStart < 0 {
		dStart = 0
	}
	high := prices[dStart]
	maxDrop := 0.0
	for _, v := range prices[dStart:] {
		if v > high {
			high = v
		}
		if high > 0 {
			if d := (high - v) / high; d > maxDrop {
				maxDrop = d
			}
		}
	}

	const velW = 5
	var velSum float64
	for i := n - velW; i < n-1; i++ {
		if prices[i] > 0 {
			velSum += (prices[i+1] - prices[i]) / prices[i]
		}
	}
	avgVel := velSum / float64(velW-1)

	var pSum float64
	for _, v := range prices[n-vw:] {
		pSum += v
	}
	pMean := pSum / float64(vw)
	var pVar float64
	for _, v := range prices[n-vw:] {
		d := v - pMean
		pVar += d * d
	}
	bbWidth := 0.0
	if pMean > 0 {
		bbWidth = 4 * math.Sqrt(pVar/float64(vw)) / pMean
	}

	sel.lastFeats = MarketFeatures{
		Volatility: vol, ER: er, Trend: trend,
		MaxDrop: maxDrop, AvgVel: avgVel, BBWidth: bbWidth,
	}
	sel.featsReady = true
}

// ══ 市场适配打分 ══════════════════════════════════════════════════════════

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

func marketScore(stratType string, f MarketFeatures) float64 {
	vol := clamp01(f.Volatility / 0.0020)
	velDn := clamp01(-f.AvgVel / 0.0005)
	velAbs := clamp01(math.Abs(f.AvgVel) / 0.0005)
	drop := clamp01(f.MaxDrop / 0.010)
	compress := clamp01(1 - f.BBWidth/0.003)
	er := f.ER
	trendStr := clamp01(math.Abs(f.Trend) / 0.0020)

	switch stratType {
	case "waterfall":
		return velDn*0.55 + vol*0.30 + (1-er)*0.15
	case "liq_trap":
		return 0.20 + vol*0.45 + (1-er)*0.35
	case "squeeze_breakout":
		return compress*0.65 + (1-vol)*0.35
	case "dead_cat_bounce":
		return drop*0.75 + vol*0.25
	case "trend_reversion_hedge":
		return velAbs*0.65 + vol*0.35
	case "trend_prob":
		return 0.25 + er*0.40 + vol*0.35
	default:
		return er*0.65 + trendStr*0.35
	}
}

// ══ 评分计算 ══════════════════════════════════════════════════════════════

// computeScores 每 tick 调用，无论特征是否就绪均返回有效评分。
// 特征未就绪时 market 字段为 -1（显示用），计算时以 0.50 中性代入。
func (sel *StratSelector) computeScores() (results []scoreResult, totals []float64) {
	n := len(sel.strategies)
	results = make([]scoreResult, n)
	totals = make([]float64, n)

	maxW := 0.001
	for _, w := range sel.histWeights {
		if w > maxW {
			maxW = w
		}
	}
	wm := sel.cfg.WeightMarket
	wh := sel.cfg.WeightHistory
	wn := sel.cfg.WeightNoise

	for i, s := range sel.strategies {
		hs := sel.histWeights[i] / maxW
		var ms, effMs float64
		if sel.featsReady {
			ms = marketScore(s.Cfg.StrategyType, sel.lastFeats)
			effMs = ms
		} else {
			ms = -1   // sentinel for display
			effMs = 0.50 // neutral market score when features unavailable
		}
		base := effMs*wm + hs*wh
		results[i] = scoreResult{market: ms, hist: hs, base: base}
		totals[i] = base + rand.Float64()*wn
	}
	return
}

func selectFromScores(totals []float64) int {
	total := 0.0
	for _, sc := range totals {
		total += sc
	}
	r := rand.Float64() * total
	cumul := 0.0
	for i, sc := range totals {
		cumul += sc
		if r <= cumul {
			return i
		}
	}
	return len(totals) - 1
}

// ══ 历史权重更新 ══════════════════════════════════════════════════════════

func (sel *StratSelector) updateWeights(idx int, pnlPct float64) {
	before := sel.histWeights[idx]
	if pnlPct > 0 {
		sel.histWeights[idx] *= math.Min(3.0, 1+pnlPct*8)
	} else {
		sel.histWeights[idx] *= math.Max(0.2, 1+pnlPct*4)
	}
	for i := range sel.histWeights {
		sel.histWeights[i] *= 0.99
	}
	color := "\033[32m"
	if pnlPct < 0 {
		color = "\033[31m"
	}
	fmt.Printf("[%s] \033[33m[选择器]\033[0m 反馈 [%s] P&L %s%+.2f%%\033[0m 权重 %.3f→%.3f\n",
		time.Now().Format("15:04:05"), sel.strategies[idx].Cfg.Name,
		color, pnlPct*100, before, sel.histWeights[idx])
	fmt.Print("  历史权重: ")
	for i, s := range sel.strategies {
		if i > 0 {
			fmt.Print("  ")
		}
		fmt.Printf("[%s:%.2f]", s.Cfg.Name, sel.histWeights[i])
	}
	fmt.Println()
}

// ══ 每 Tick 显示 ══════════════════════════════════════════════════════════

func scoreColor(v float64) string {
	if v >= 0.65 {
		return "\033[32m"
	} else if v >= 0.40 {
		return "\033[33m"
	}
	return "\033[31m"
}

func (sel *StratSelector) printTickStatus(
	tick tradelib.Tick, endTime time.Time,
	scores []scoreResult, newlySelected bool,
) {
	remaining := time.Until(endTime).Round(time.Second)
	ts := time.Now().Format("15:04:05")

	// ── 总资金 + 价格行 ────────────────────────────────────────────────
	capChange := sel.totalCapital - sel.initialCapital
	capChangePct := capChange / sel.initialCapital * 100
	capColor := "\033[32m"
	if capChange < 0 {
		capColor = "\033[31m"
	}
	fmt.Printf("[%s] BTC $%.2f (B:%.2f A:%.2f) | 总资金:%s$%.2f(%+.2f%%)\033[0m | 剩余:%v\n",
		ts, tick.Prc, tick.Bid1, tick.Ask1,
		capColor, sel.totalCapital, capChangePct, remaining)

	// ── 市场特征行 ─────────────────────────────────────────────────────
	if sel.featsReady {
		f := sel.lastFeats
		trendTag := fmt.Sprintf("\033[33m→%+.3f%%\033[0m", f.Trend*100)
		if f.Trend > 0.0001 {
			trendTag = fmt.Sprintf("\033[32m↑%+.3f%%\033[0m", f.Trend*100)
		} else if f.Trend < -0.0001 {
			trendTag = fmt.Sprintf("\033[31m↓%+.3f%%\033[0m", f.Trend*100)
		}
		fmt.Printf("  特征: 波动:%.3f%% ER:%.2f 趋:%s 跌:%.2f%% BB:%.3f%%\n",
			f.Volatility*100, f.ER, trendTag, f.MaxDrop*100, f.BBWidth*100)
	} else {
		// 显示数据来源状态，让用户知道在积累中
		fmt.Printf("  特征: 积累中(内存%d格) | 市场分以\033[33m0.50\033[0m中性代替，评分已按历史权重激活策略\n",
			len(sel.prices))
	}

	// ── 策略评分表 ─────────────────────────────────────────────────────
	for i, s := range sel.strategies {
		isActive := i == sel.activeIdx

		marker := "  "
		if isActive {
			marker = "\033[36m▶\033[0m "
		}

		var scoreStr string
		if scores == nil {
			scoreStr = "市:──── 史:──── 总:────"
		} else {
			sc := scores[i]
			if sc.market < 0 {
				// 特征未就绪：市场分显示为 ?，但总分有效（用 0.50 计算）
				scoreStr = fmt.Sprintf("市:\033[33m0.50\033[0m? 史:%.2f 总:%s%.2f\033[0m",
					sc.hist, scoreColor(sc.base), sc.base)
			} else {
				scoreStr = fmt.Sprintf("市:%s%.2f\033[0m 史:%.2f 总:%s%.2f\033[0m",
					scoreColor(sc.market), sc.market, sc.hist, scoreColor(sc.base), sc.base)
			}
		}

		statusStr := ""
		if isActive {
			eq := s.CurrentEquity(tick)
			pnl := eq - sel.totalCapital
			pnlPct := pnl / sel.totalCapital * 100
			if s.InPosition() {
				pnlColor := "\033[32m"
				if pnl < 0 {
					pnlColor = "\033[31m"
				}
				statusStr = fmt.Sprintf(" \033[36m│\033[0m 持仓中 $%.2f %s%+.2f%%\033[0m",
					eq, pnlColor, pnlPct)
			} else if newlySelected {
				statusStr = " \033[36m│\033[0m \033[33m← 刚选中\033[0m"
			} else {
				statusStr = fmt.Sprintf(" \033[36m│\033[0m 等待信号(%d/%d格)",
					sel.patienceTick, sel.cfg.MaxPatience)
			}
		}

		fmt.Printf("%s[%-10s] %s%s\n", marker, s.Cfg.Name, scoreStr, statusStr)
	}
}

// ══ 主程序 ════════════════════════════════════════════════════════════════

func main() {
	selectorPath := "selector.json"
	fallbackPath := "config.json"
	if len(os.Args) > 1 {
		selectorPath = os.Args[1]
	}
	if len(os.Args) > 2 {
		fallbackPath = os.Args[2]
	}

	cfg, _ := loadSelectorConfig(selectorPath, fallbackPath)

	// 加载历史数据用于策略指标预热 + 内存缓冲初始化
	historyF := tradelib.LoadHistoricalPricesFromDir(cfg.DataDir, cfg.LookbackHours)
	if len(historyF) > 0 {
		log.Printf("策略预热：%s/ 加载 %d 条历史价格\n", cfg.DataDir, len(historyF))
	} else {
		log.Printf("未找到历史数据（%s/），将从实时 tick 积累价格缓冲\n", cfg.DataDir)
	}

	// 创建所有策略（Quiet 模式，统一使用总资金）
	var strategies []*tradelib.Strategy
	for _, sc := range cfg.Strategies {
		if sc.Disabled {
			continue
		}
		sc.Quiet = true
		sc.InitialCapital = cfg.TotalCapital
		strategies = append(strategies, tradelib.NewStrategy(sc, historyF))
	}
	if len(strategies) == 0 {
		log.Fatal("无可用策略，请检查配置")
	}

	sel := newSelector(&cfg, strategies)

	// 用历史价格初始化内存缓冲（让特征尽快就绪）
	if len(historyF) > 0 {
		buf := historyF
		if len(buf) > 300 {
			buf = buf[len(buf)-300:]
		}
		sel.prices = make([]float64, len(buf))
		copy(sel.prices, buf)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	startTime := time.Now()
	endTime := startTime.Add(cfg.TradeDuration())
	var lastTick tradelib.Tick

	fmt.Println("========== BTC 动态策略选择器 ==========")
	fmt.Printf("总资金: $%.2f（全量再投入，盈亏实时复利）\n", cfg.TotalCapital)
	fmt.Printf("权重: 市场%.0f%% 历史%.0f%% 扰动%.0f%% | 等待容忍: %d格\n",
		cfg.WeightMarket*100, cfg.WeightHistory*100, cfg.WeightNoise*100, cfg.MaxPatience)
	fmt.Printf("数据目录: %s | 预热: %d条\n", cfg.DataDir, len(historyF))
	for _, s := range strategies {
		fmt.Printf("  [%-10s] %-22s 杠杆:%.0fx SL:%.1f%% TP:%.1f%%\n",
			s.Cfg.Name, s.Cfg.StrategyType, s.Cfg.Leverage,
			s.Cfg.StopLoss*100, s.Cfg.TakeProfit*100)
	}
	fmt.Printf("运行时长: %v | 采样: %v\n", cfg.TradeDuration(), cfg.SampleInterval())
	fmt.Printf("注：特征未就绪前市场分以 0.50 中性代替，选择器从第1个tick起即激活策略\n")
	fmt.Println("=========================================")

	ticker := time.NewTicker(cfg.SampleInterval())
	defer ticker.Stop()
	timer := time.NewTimer(cfg.TradeDuration())
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			tick, err := tradelib.FetchPrice()
			if err != nil {
				tick = lastTick
			}
			fmt.Println("\n──── 时间到期，强制平仓 ────")
			for _, s := range strategies {
				s.ForceLiquidate(tick, "到期")
			}
			if sel.activeIdx >= 0 {
				sel.totalCapital = strategies[sel.activeIdx].CurrentEquity(tick)
			}
			finalPnl := sel.totalCapital - sel.initialCapital
			finalPct := finalPnl / sel.initialCapital * 100
			color := "\033[32m"
			if finalPnl < 0 {
				color = "\033[31m"
			}
			fmt.Printf("总资金: $%.2f → $%.2f (%s%+.2f%%\033[0m)\n",
				sel.initialCapital, sel.totalCapital, color, finalPct)
			tradelib.PrintAllReports(strategies, startTime, tick)
			return

		case sv := <-sig:
			fmt.Printf("\n[%s] 收到退出信号 (%v)，正在结算...\n",
				time.Now().Format("15:04:05"), sv)
			for _, s := range strategies {
				s.ForceLiquidate(lastTick, "中断退出")
			}
			if sel.activeIdx >= 0 {
				sel.totalCapital = strategies[sel.activeIdx].CurrentEquity(lastTick)
			}
			finalPnl := sel.totalCapital - sel.initialCapital
			finalPct := finalPnl / sel.initialCapital * 100
			color := "\033[32m"
			if finalPnl < 0 {
				color = "\033[31m"
			}
			fmt.Printf("总资金: $%.2f → $%.2f (%s%+.2f%%\033[0m)\n",
				sel.initialCapital, sel.totalCapital, color, finalPct)
			tradelib.PrintAllReports(strategies, startTime, lastTick)
			return

		case <-ticker.C:
			tick, err := tradelib.FetchPrice()
			if err != nil {
				log.Printf("获取价格失败: %v\n", err)
				continue
			}
			lastTick = tick

			// ── 步骤1：更新价格缓冲 + 刷新市场特征 ─────────────────────
			sel.refreshFeatures(tick.Prc)

			// ── 步骤2：检测激活策略的仓位变化 ───────────────────────────
			if sel.activeIdx >= 0 {
				active := sel.strategies[sel.activeIdx]
				nowInPos := active.InPosition()

				if sel.prevInPos && !nowInPos {
					curEq := active.CurrentEquity(tick)
					pnlPct := 0.0
					if sel.openEq > 0 {
						pnlPct = (curEq - sel.openEq) / sel.openEq
					}
					sel.totalCapital = curEq
					sel.updateWeights(sel.activeIdx, pnlPct)
					sel.activeIdx = -1
					sel.prevInPos = false
					sel.patienceTick = 0
				} else if !sel.prevInPos && nowInPos {
					sel.openEq = active.CurrentEquity(tick)
					sel.patienceTick = 0
				} else if !nowInPos {
					sel.patienceTick++
					if sel.patienceTick >= sel.cfg.MaxPatience {
						fmt.Printf("[%s] \033[33m[选择器]\033[0m [%s] 等待%d格无信号，重新选择\n",
							time.Now().Format("15:04:05"), active.Cfg.Name, sel.patienceTick)
						sel.activeIdx = -1
						sel.patienceTick = 0
					}
				}
				sel.prevInPos = nowInPos
			}

			// ── 步骤3：每 tick 计算评分（特征未就绪时市场分用 0.50 代替）
			scores, totals := sel.computeScores()

			// ── 步骤4：无激活策略时选择（不再等待 featsReady，始终激活）─
			newlySelected := false
			if sel.activeIdx == -1 {
				sel.activeIdx = selectFromScores(totals)
				active := sel.strategies[sel.activeIdx]
				active.SetCapital(sel.totalCapital)
				sel.prevInPos = active.InPosition()
				sel.patienceTick = 0
				newlySelected = true
				featTag := "特征就绪"
				if !sel.featsReady {
					featTag = "\033[33m特征积累中\033[0m"
				}
				fmt.Printf("[%s] \033[36m[选择器] 激活: [%s] (%s)\033[0m 注入:$%.2f 历史权重:%.2f [%s]\n",
					time.Now().Format("15:04:05"), active.Cfg.Name,
					active.Cfg.StrategyType, sel.totalCapital,
					sel.histWeights[sel.activeIdx], featTag)
			}

			// ── 步骤5：价格分发 ───────────────────────────────────────────
			for i, s := range sel.strategies {
				if i == sel.activeIdx {
					s.OnPrice(tick, endTime)
				} else {
					s.FeedOnly(tick)
				}
			}

			// ── 步骤6：打印状态 ───────────────────────────────────────────
			sel.printTickStatus(tick, endTime, scores, newlySelected)
		}
	}
}
