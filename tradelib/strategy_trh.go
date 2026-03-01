package tradelib

import (
	"fmt"
	"math"
	"time"
)

// ===== TRH 状态机类型 =====

type trhState int8

const (
	trhIdle      trhState = 0 // 空闲，等待信号
	trhDetectedA trhState = 1 // A型检测到，等待入场确认（反转tick）
	trhDetectedB trhState = 2 // B型检测到，等待稳定确认
	trhDualHold  trhState = 3 // 双腿持仓中
	trhCooldown  trhState = 5 // B型平仓冷却（A型无冷却，直接回Idle）
)

type trhMode int8

const (
	trhModeNone        trhMode = 0
	trhModeFlashRevert trhMode = 1 // A型：闪崩/闪涨均值回归（逆势主腿）
	trhModeTrendFollow trhMode = 2 // B型：宏观趋势顺势对冲（顺势主腿）
)

// dualPortfolio 持有主腿（larger）和对冲腿（smaller）两个独立仓位
type dualPortfolio struct {
	state trhState
	mode  trhMode

	trendDir     posDir  // 检测到的原始趋势方向（crash=dirShort，rally=dirLong）
	trendMovePct float64 // 检测到的幅度（正数）
	velocity     float64 // 速率（fraction/min）

	majorDir     posDir
	majorCapital float64
	majorLeg     portfolio

	hedgeCapital float64
	hedgeLeg     portfolio

	entryTime     time.Time
	cooldownUntil time.Time

	// B型稳定检测：连续满足条件的tick数
	stabilityCount int
}

// ===== 主处理函数 =====

// onPriceTRH 趋势反转对冲策略处理器
// A型（TRHFastWindowTicks>0）：闪崩/闪涨后快速均值回归，无Kalman，15分钟内快进快出
// B型（TRHSlowWindowTicks>0）：宏观趋势顺势对冲，Kalman辅助稳定确认
func (s *Strategy) onPriceTRH(tick Tick, endTime time.Time) {
	price := tick.Prc
	s.feedPrice(price)

	// Kalman（B型稳定确认辅助；A型 kalman_r=0 自动禁用）
	kVelPct := 0.0
	if s.Cfg.KalmanR > 0 {
		p2 := price * price
		_, vel := s.kf.step(price,
			p2*s.Cfg.KalmanQPos*s.Cfg.KalmanQPos,
			p2*s.Cfg.KalmanQVel*s.Cfg.KalmanQVel,
			p2*s.Cfg.KalmanR*s.Cfg.KalmanR)
		kVelPct = vel / price
	}

	n := s.priceBuf.count
	if n < s.warmupNeed {
		if !s.Cfg.Quiet {
			fmt.Printf("[%s] [%-5s] 预热中 $%.2f (%d/%d)\n",
				time.Now().Format("15:04:05"), s.Cfg.Name, price, n, s.warmupNeed)
		}
		return
	}

	dp := &s.trhDual
	equity := s.trhTotalEquity(tick)
	if equity > s.p.peakEquity {
		s.p.peakEquity = equity
	}

	switch dp.state {
	case trhIdle:
		s.trhHandleIdle(tick, kVelPct, endTime)
	case trhDetectedA:
		s.trhHandleDetectedA(tick, endTime)
	case trhDetectedB:
		s.trhHandleDetectedB(tick, kVelPct, endTime)
	case trhDualHold:
		s.trhHandleDualHold(tick, endTime)
	case trhCooldown:
		s.trhHandleCooldown(tick, endTime)
	}
}

// trhHandleIdle 空闲状态：优先尝试A型检测，其次B型检测
func (s *Strategy) trhHandleIdle(tick Tick, kVelPct float64, endTime time.Time) {
	dp := &s.trhDual
	price := tick.Prc
	n := s.priceBuf.count

	// ─── A型检测（零延迟，原始价格比较）───
	if s.Cfg.TRHFastWindowTicks > 0 && n >= s.Cfg.TRHFastWindowTicks {
		oldest := s.priceBuf.Get(s.Cfg.TRHFastWindowTicks - 1)
		if oldest > 0 {
			rawMove := (price - oldest) / oldest
			absMov := math.Abs(rawMove)
			if absMov >= s.Cfg.TRHFastMoveThreshold {
				dp.state = trhDetectedA
				dp.mode = trhModeFlashRevert
				if rawMove < 0 {
					dp.trendDir = dirShort // 价格向下（flash crash）
				} else {
					dp.trendDir = dirLong // 价格向上（flash rally）
				}
				dp.trendMovePct = absMov
				dp.velocity = absMov / (float64(s.Cfg.TRHFastWindowTicks) * 5.0 / 60.0)
				fmt.Printf("[%s] [%-5s] \033[35m[TRH-A检测]\033[0m 方向:%s 幅度:%.2f%% 速率:%.4f%%/min\n",
					time.Now().Format("15:04:05"), s.Cfg.Name,
					dp.trendDir, dp.trendMovePct*100, dp.velocity*100)
				s.trhHandleDetectedA(tick, endTime)
				return
			}
		}
	}

	// ─── B型检测（Kalman辅助，12小时窗口）───
	if s.Cfg.TRHSlowWindowTicks > 0 && n >= s.Cfg.TRHSlowWindowTicks {
		oldest := s.priceBuf.Get(s.Cfg.TRHSlowWindowTicks - 1)
		if oldest > 0 {
			rawMove := (price - oldest) / oldest
			absMov := math.Abs(rawMove)
			windowMin := float64(s.Cfg.TRHSlowWindowTicks) * 5.0 / 60.0
			velocity := absMov / windowMin
			er := calcER(s.priceBuf, s.Cfg.TRHSlowWindowTicks)

			if absMov >= s.Cfg.TRHSlowMoveThreshold &&
				(s.Cfg.TRHSlowVelMax <= 0 || velocity <= s.Cfg.TRHSlowVelMax) &&
				(s.Cfg.TRHERThreshold <= 0 || er >= s.Cfg.TRHERThreshold) {
				dp.state = trhDetectedB
				dp.mode = trhModeTrendFollow
				if rawMove < 0 {
					dp.trendDir = dirShort
				} else {
					dp.trendDir = dirLong
				}
				dp.trendMovePct = absMov
				dp.velocity = velocity
				dp.stabilityCount = 0
				fmt.Printf("[%s] [%-5s] \033[35m[TRH-B检测]\033[0m 方向:%s 幅度:%.2f%% 速率:%.5f%%/min ER:%.2f\n",
					time.Now().Format("15:04:05"), s.Cfg.Name,
					dp.trendDir, dp.trendMovePct*100, dp.velocity*100, er)
				s.trhHandleDetectedB(tick, kVelPct, endTime)
				return
			}
		}
	}

	s.trhPrintStatus(tick, endTime, kVelPct, "等待信号")
}

// trhHandleDetectedA A型检测后等待入场确认
func (s *Strategy) trhHandleDetectedA(tick Tick, endTime time.Time) {
	dp := &s.trhDual
	price := tick.Prc
	n := s.priceBuf.count

	// 重新验证A型幅度（信号失效则回Idle）
	if s.Cfg.TRHFastWindowTicks > 0 && n >= s.Cfg.TRHFastWindowTicks {
		oldest := s.priceBuf.Get(s.Cfg.TRHFastWindowTicks - 1)
		if oldest > 0 {
			rawMove := (price - oldest) / oldest
			if math.Abs(rawMove) < s.Cfg.TRHFastMoveThreshold*0.5 {
				dp.state = trhIdle
				s.trhPrintStatus(tick, endTime, 0, "A型信号失效（幅度已恢复）")
				return
			}
		}
	}

	// 瀑布结束检测：最近5个tick中，开头连续下跌数 < 3
	checkN := 5
	if n < checkN {
		checkN = n
	}
	consecutiveDown := 0
	for i := 0; i < checkN-1; i++ {
		if s.priceBuf.Get(i) < s.priceBuf.Get(i+1) {
			consecutiveDown++
		} else {
			break
		}
	}
	waterfallEnded := consecutiveDown < 3

	// 反转tick确认
	var firstReversal bool
	if n >= 2 {
		if dp.trendDir == dirShort {
			firstReversal = s.priceBuf.Get(0) > s.priceBuf.Get(1) // crash后价格首次上涨
		} else {
			firstReversal = s.priceBuf.Get(0) < s.priceBuf.Get(1) // rally后价格首次下跌
		}
	}

	if firstReversal && waterfallEnded {
		// 添加 Kalman 速度方向验证（如果配置开启）
		if s.Cfg.KalmanVelThresh > 0 && s.Cfg.KalmanR > 0 {
			p2 := price * price
			_, vel := s.kf.step(price,
				p2*s.Cfg.KalmanQPos*s.Cfg.KalmanQPos,
				p2*s.Cfg.KalmanQVel*s.Cfg.KalmanQVel,
				p2*s.Cfg.KalmanR*s.Cfg.KalmanR)
			kVelPct := vel / price

			// 对于做多闪崩回归（趋势向下，回归向上），Kalman速度必须向上
			if dp.trendDir == dirShort && kVelPct < s.Cfg.KalmanVelThresh {
				s.trhPrintStatus(tick, endTime, 0, "等A型入场(K方向不符，需向上)")
				return
			}
			// 对于做空冲高回归（趋势向上，回归向下），Kalman速度必须向下
			if dp.trendDir == dirLong && kVelPct > -s.Cfg.KalmanVelThresh {
				s.trhPrintStatus(tick, endTime, 0, "等A型入场(K方向不符，需向下)")
				return
			}
		}

		s.trhEnterDual(tick, trhModeFlashRevert)
		return
	}

	ck := func(ok bool) string {
		if ok {
			return "\033[32m✓\033[0m"
		}
		return "\033[31m✗\033[0m"
	}
	signal := fmt.Sprintf("等A型入场 反转%s 瀑布结束%s(连跌%d格)",
		ck(firstReversal), ck(waterfallEnded), consecutiveDown)
	s.trhPrintStatus(tick, endTime, 0, signal)
}

// trhHandleDetectedB B型检测后等待稳定确认
func (s *Strategy) trhHandleDetectedB(tick Tick, kVelPct float64, endTime time.Time) {
	dp := &s.trhDual
	price := tick.Prc

	stableTicks := s.Cfg.TRHSlowStableTicks
	if stableTicks <= 0 {
		stableTicks = 360
	}

	atr := calcATR(s.priceBuf, stableTicks)
	volatility := calcVolatility(s.priceBuf, stableTicks)
	er := calcER(s.priceBuf, stableTicks)

	atrPct := 0.0
	if price > 0 {
		atrPct = atr / price
	}

	volOK := s.Cfg.TRHStabilityVolThr <= 0 || atrPct < s.Cfg.TRHStabilityVolThr
	rangeOK := s.Cfg.TRHStabilityRangePct <= 0 || volatility < s.Cfg.TRHStabilityRangePct
	erOK := s.Cfg.TRHERStableMax <= 0 || er < s.Cfg.TRHERStableMax
	kalmanOK := s.Cfg.TRHKalmanVelStable <= 0 || math.Abs(kVelPct) < s.Cfg.TRHKalmanVelStable

	if volOK && rangeOK && erOK && kalmanOK {
		dp.stabilityCount++
	} else {
		dp.stabilityCount = 0
	}

	if dp.stabilityCount >= 3 {
		s.trhEnterDual(tick, trhModeTrendFollow)
		return
	}

	ck := func(ok bool) string {
		if ok {
			return "\033[32m✓\033[0m"
		}
		return "\033[31m✗\033[0m"
	}
	signal := fmt.Sprintf("等B型稳定 ATR%s(%.4f%%) 振幅%s(%.3f%%) ER%s(%.2f) K%s(%.5f%%) 计数:%d/3",
		ck(volOK), atrPct*100, ck(rangeOK), volatility*100,
		ck(erOK), er, ck(kalmanOK), math.Abs(kVelPct)*100, dp.stabilityCount)
	s.trhPrintStatus(tick, endTime, kVelPct, signal)
}

// trhEnterDual 开双腿仓位
func (s *Strategy) trhEnterDual(tick Tick, mode trhMode) {
	dp := &s.trhDual
	dp.mode = mode

	totalCapital := s.p.cash
	if totalCapital <= 0 {
		totalCapital = s.Cfg.InitialCapital
	}

	var majorDir, hedgeDir posDir
	var majorRatio float64

	if mode == trhModeFlashRevert {
		majorRatio = s.Cfg.TRHMajorRatioA
		if majorRatio <= 0 {
			majorRatio = 0.70
		}
		if dp.trendDir == dirShort {
			majorDir = dirLong
			hedgeDir = dirShort
		} else {
			majorDir = dirShort
			hedgeDir = dirLong
		}
	} else {
		majorRatio = s.Cfg.TRHMajorRatioB
		if majorRatio <= 0 {
			majorRatio = 0.65
		}
		if dp.trendDir == dirShort {
			majorDir = dirShort
			hedgeDir = dirLong
		} else {
			majorDir = dirLong
			hedgeDir = dirShort
		}
	}

	dp.majorDir = majorDir
	dp.majorCapital = totalCapital * majorRatio
	dp.hedgeCapital = totalCapital * (1 - majorRatio)

	dp.majorLeg = portfolio{name: s.Cfg.Name + "主", cash: dp.majorCapital, peakEquity: dp.majorCapital}
	dp.hedgeLeg = portfolio{name: s.Cfg.Name + "对", cash: dp.hedgeCapital, peakEquity: dp.hedgeCapital}

	majorCfg := s.Cfg
	hedgeCfg := s.Cfg
	if mode == trhModeFlashRevert {
		if s.Cfg.TRHMajorLeverageA > 0 {
			majorCfg.Leverage = s.Cfg.TRHMajorLeverageA
		}
		if s.Cfg.TRHHedgeLeverageA > 0 {
			hedgeCfg.Leverage = s.Cfg.TRHHedgeLeverageA
		}
	} else {
		if s.Cfg.TRHMajorLeverageB > 0 {
			majorCfg.Leverage = s.Cfg.TRHMajorLeverageB
		}
		if s.Cfg.TRHHedgeLeverageB > 0 {
			hedgeCfg.Leverage = s.Cfg.TRHHedgeLeverageB
		}
	}

	dp.majorLeg.openPosVolAdjusted(majorCfg, majorDir, tick, 0.015, &s.trades)
	dp.hedgeLeg.openPosVolAdjusted(hedgeCfg, hedgeDir, tick, 0.015, &s.trades)

	dp.state = trhDualHold
	dp.entryTime = time.Now()
	s.p.cash = 0

	modeStr := "A型闪回"
	if mode == trhModeTrendFollow {
		modeStr = "B型趋势"
	}
	fmt.Printf("[%s] [%-5s] \033[36m[TRH入场-%s]\033[0m 主腿%s $%.2f×%.1fx | 对冲%s $%.2f×%.1fx | 总资金$%.2f\n",
		time.Now().Format("15:04:05"), s.Cfg.Name, modeStr,
		majorDir, dp.majorCapital, majorCfg.Leverage,
		hedgeDir, dp.hedgeCapital, hedgeCfg.Leverage, totalCapital)
}

// trhHandleDualHold 持仓管理分派
func (s *Strategy) trhHandleDualHold(tick Tick, endTime time.Time) {
	if s.trhDual.mode == trhModeFlashRevert {
		s.trhManageDualHoldA(tick, endTime)
	} else {
		s.trhManageDualHoldB(tick, endTime)
	}
}

// trhManageDualHoldA A型持仓管理（超时，合计TP/SL，单腿超额）
func (s *Strategy) trhManageDualHoldA(tick Tick, endTime time.Time) {
	dp := &s.trhDual
	totalInit := dp.majorCapital + dp.hedgeCapital
	totalEquity := dp.majorLeg.totalEquity(s.Cfg, tick) + dp.hedgeLeg.totalEquity(s.Cfg, tick)
	totalPnlPct := (totalEquity - totalInit) / totalInit

	majorPct := dp.majorLeg.positionPct(tick)
	hedgePct := dp.hedgeLeg.positionPct(tick)
	holdSec := time.Since(dp.entryTime).Seconds()

	if s.Cfg.TRHMaxHoldSecA > 0 && holdSec >= float64(s.Cfg.TRHMaxHoldSecA) {
		s.trhCloseBothA(tick, fmt.Sprintf("超时(%.0fs)", holdSec))
		return
	}
	if s.Cfg.TRHTotalTakeProfitA > 0 && totalPnlPct >= s.Cfg.TRHTotalTakeProfitA {
		s.trhCloseBothA(tick, fmt.Sprintf("合计止盈+%.2f%%", totalPnlPct*100))
		return
	}
	if s.Cfg.TRHTotalStopLossA > 0 && totalPnlPct <= -s.Cfg.TRHTotalStopLossA {
		s.trhCloseBothA(tick, fmt.Sprintf("合计止损%.2f%%", totalPnlPct*100))
		return
	}
	if s.Cfg.TRHLegExcessProfitA > 0 && majorPct >= s.Cfg.TRHLegExcessProfitA {
		s.trhCloseBothA(tick, fmt.Sprintf("主腿超额+%.2f%%", majorPct*100))
		return
	}
	if s.Cfg.TRHLegExcessProfitA > 0 && hedgePct >= s.Cfg.TRHLegExcessProfitA {
		s.trhCloseBothA(tick, fmt.Sprintf("对冲超额+%.2f%%", hedgePct*100))
		return
	}

	signal := fmt.Sprintf("A型持仓 合计%+.2f%% 主腿%+.2f%% 对冲%+.2f%% 已持%.0fs/%ds",
		totalPnlPct*100, majorPct*100, hedgePct*100, holdSec, s.Cfg.TRHMaxHoldSecA)
	s.trhPrintStatus(tick, endTime, 0, signal)
}

// trhManageDualHoldB B型持仓管理（72小时超时，各腿独立止损，合计TP/SL，单腿超额）
func (s *Strategy) trhManageDualHoldB(tick Tick, endTime time.Time) {
	dp := &s.trhDual
	totalInit := dp.majorCapital + dp.hedgeCapital
	totalEquity := dp.majorLeg.totalEquity(s.Cfg, tick) + dp.hedgeLeg.totalEquity(s.Cfg, tick)
	totalPnlPct := (totalEquity - totalInit) / totalInit

	majorPct := dp.majorLeg.positionPct(tick)
	hedgePct := dp.hedgeLeg.positionPct(tick)
	holdSec := time.Since(dp.entryTime).Seconds()
	maxHoldSec := float64(s.Cfg.TRHMaxHoldHoursB) * 3600

	if s.Cfg.TRHMaxHoldHoursB > 0 && holdSec >= maxHoldSec {
		s.trhCloseBothB(tick, fmt.Sprintf("超时(%.1fh)", holdSec/3600))
		return
	}
	if s.Cfg.TRHTotalTakeProfitB > 0 && totalPnlPct >= s.Cfg.TRHTotalTakeProfitB {
		s.trhCloseBothB(tick, fmt.Sprintf("合计止盈+%.2f%%", totalPnlPct*100))
		return
	}
	if s.Cfg.TRHTotalStopLossB > 0 && totalPnlPct <= -s.Cfg.TRHTotalStopLossB {
		s.trhCloseBothB(tick, fmt.Sprintf("合计止损%.2f%%", totalPnlPct*100))
		return
	}
	if s.Cfg.TRHMajorStopLossB > 0 && majorPct <= -s.Cfg.TRHMajorStopLossB {
		s.trhCloseBothB(tick, fmt.Sprintf("主腿止损%.2f%%", majorPct*100))
		return
	}
	if s.Cfg.TRHHedgeStopLoss > 0 && hedgePct <= -s.Cfg.TRHHedgeStopLoss {
		s.trhCloseBothB(tick, fmt.Sprintf("对冲止损%.2f%%", hedgePct*100))
		return
	}
	if s.Cfg.TRHLegExcessProfitB > 0 && majorPct >= s.Cfg.TRHLegExcessProfitB {
		s.trhCloseBothB(tick, fmt.Sprintf("主腿超额+%.2f%%", majorPct*100))
		return
	}
	if s.Cfg.TRHLegExcessProfitB > 0 && hedgePct >= s.Cfg.TRHLegExcessProfitB {
		s.trhCloseBothB(tick, fmt.Sprintf("对冲超额+%.2f%%", hedgePct*100))
		return
	}

	maxHoldH := 0.0
	if s.Cfg.TRHMaxHoldHoursB > 0 {
		maxHoldH = maxHoldSec / 3600
	}
	signal := fmt.Sprintf("B型持仓 合计%+.2f%% 主腿%+.2f%% 对冲%+.2f%% 已持%.1fh/%.0fh",
		totalPnlPct*100, majorPct*100, hedgePct*100, holdSec/3600, maxHoldH)
	s.trhPrintStatus(tick, endTime, 0, signal)
}

// trhCloseBothA A型平仓（无冷却，直接回Idle）
func (s *Strategy) trhCloseBothA(tick Tick, reason string) {
	dp := &s.trhDual

	majorCfg := s.Cfg
	hedgeCfg := s.Cfg
	if s.Cfg.TRHMajorLeverageA > 0 {
		majorCfg.Leverage = s.Cfg.TRHMajorLeverageA
	}
	if s.Cfg.TRHHedgeLeverageA > 0 {
		hedgeCfg.Leverage = s.Cfg.TRHHedgeLeverageA
	}

	if dp.majorLeg.inPosition() {
		dp.majorLeg.closePos(majorCfg, tick, reason, &s.trades)
	}
	if dp.hedgeLeg.inPosition() {
		dp.hedgeLeg.closePos(hedgeCfg, tick, reason, &s.trades)
	}

	totalInit := dp.majorCapital + dp.hedgeCapital
	totalFinal := dp.majorLeg.cash + dp.hedgeLeg.cash
	pnlPct := 0.0
	if totalInit > 0 {
		pnlPct = (totalFinal - totalInit) / totalInit * 100
	}

	fmt.Printf("[%s] [%-5s] \033[33m[TRH-A平仓:%s]\033[0m 合计%+.2f%% $%.2f→$%.2f\n",
		time.Now().Format("15:04:05"), s.Cfg.Name, reason, pnlPct, totalInit, totalFinal)

	s.p.cash = totalFinal
	dp.state = trhIdle
	dp.mode = trhModeNone
	dp.majorLeg = portfolio{}
	dp.hedgeLeg = portfolio{}
}

// trhCloseBothB B型平仓（进入冷却期）
func (s *Strategy) trhCloseBothB(tick Tick, reason string) {
	dp := &s.trhDual

	majorCfg := s.Cfg
	hedgeCfg := s.Cfg
	if s.Cfg.TRHMajorLeverageB > 0 {
		majorCfg.Leverage = s.Cfg.TRHMajorLeverageB
	}
	if s.Cfg.TRHHedgeLeverageB > 0 {
		hedgeCfg.Leverage = s.Cfg.TRHHedgeLeverageB
	}

	if dp.majorLeg.inPosition() {
		dp.majorLeg.closePos(majorCfg, tick, reason, &s.trades)
	}
	if dp.hedgeLeg.inPosition() {
		dp.hedgeLeg.closePos(hedgeCfg, tick, reason, &s.trades)
	}

	totalInit := dp.majorCapital + dp.hedgeCapital
	totalFinal := dp.majorLeg.cash + dp.hedgeLeg.cash
	pnlPct := 0.0
	if totalInit > 0 {
		pnlPct = (totalFinal - totalInit) / totalInit * 100
	}

	cooldownSec := s.Cfg.TRHCooldownSecB
	if cooldownSec <= 0 {
		cooldownSec = 3600
	}
	dp.cooldownUntil = time.Now().Add(time.Duration(cooldownSec) * time.Second)

	fmt.Printf("[%s] [%-5s] \033[33m[TRH-B平仓:%s]\033[0m 合计%+.2f%% $%.2f→$%.2f 冷却%ds\n",
		time.Now().Format("15:04:05"), s.Cfg.Name, reason, pnlPct, totalInit, totalFinal, cooldownSec)

	s.p.cash = totalFinal
	dp.state = trhCooldown
	dp.mode = trhModeNone
	dp.majorLeg = portfolio{}
	dp.hedgeLeg = portfolio{}
}

// trhHandleCooldown 冷却期处理（B型专用）
func (s *Strategy) trhHandleCooldown(tick Tick, endTime time.Time) {
	dp := &s.trhDual
	if time.Now().After(dp.cooldownUntil) {
		dp.state = trhIdle
		s.trhPrintStatus(tick, endTime, 0, "冷却结束，重新扫描")
		return
	}
	remaining := time.Until(dp.cooldownUntil).Round(time.Second)
	s.trhPrintStatus(tick, endTime, 0, fmt.Sprintf("B型冷却中 剩余%v", remaining))
}

// trhTotalEquity 计算TRH当前总权益
func (s *Strategy) trhTotalEquity(tick Tick) float64 {
	dp := &s.trhDual
	if dp.state == trhDualHold {
		return dp.majorLeg.totalEquity(s.Cfg, tick) + dp.hedgeLeg.totalEquity(s.Cfg, tick)
	}
	return s.p.cash
}

// trhPrintStatus TRH状态行打印
func (s *Strategy) trhPrintStatus(tick Tick, endTime time.Time, kVelPct float64, signal string) {
	if s.Cfg.Quiet {
		return
	}
	dp := &s.trhDual
	equity := s.trhTotalEquity(tick)
	pnl := equity - s.Cfg.InitialCapital
	pnlPct := pnl / s.Cfg.InitialCapital * 100
	remaining := time.Until(endTime).Round(time.Second)

	stateStr := "空闲"
	switch dp.state {
	case trhDetectedA:
		stateStr = "A型待入"
	case trhDetectedB:
		stateStr = "B型待稳"
	case trhDualHold:
		stateStr = "双腿持仓"
	case trhCooldown:
		stateStr = "冷却中"
	}

	kStr := ""
	if s.Cfg.KalmanR > 0 {
		kColor := "\033[33m"
		if kVelPct > 0 {
			kColor = "\033[32m"
		} else if kVelPct < 0 {
			kColor = "\033[31m"
		}
		kStr = fmt.Sprintf("K:%s%+.4f%%\033[0m ", kColor, kVelPct*100)
	}

	fmt.Printf("[%s] [%-5s] $%.2f %s[%s] | 权益:$%.2f(%+.2f%%) | 剩余:%v | %s\n",
		time.Now().Format("15:04:05"), s.Cfg.Name, tick.Prc, kStr,
		stateStr, equity, pnlPct, remaining, signal)
}
