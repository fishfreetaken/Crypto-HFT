package main

import (
	"fmt"
	"time"
)

// onPriceDeadCatBounce 死猫反弹策略：
//
// 原理（急跌→弱反弹→续跌）：
//   - 检测窗口内出现有效急跌（高点→低点 ≥ DCBDropMinPct）
//   - 低点后出现小幅反弹（≥ DCBBounceMinPct），但反弹幅度不超过总跌幅的 DCBBounceMaxPct
//   - 反弹结束时（连续 DCBConfirmTicks 个 tick 下跌）做空入场
//   - 只做空：死猫反弹专指下跌行情中的弱反弹，不做多
func (s *Strategy) onPriceDeadCatBounce(price float64, endTime time.Time) {
	s.prices = append(s.prices, price)

	// Kalman 滤波（预热期间也运行）
	kVelPct := 0.0
	if s.cfg.KalmanR > 0 {
		p2 := price * price
		_, vel := s.kf.step(price,
			p2*s.cfg.KalmanQPos*s.cfg.KalmanQPos,
			p2*s.cfg.KalmanQVel*s.cfg.KalmanQVel,
			p2*s.cfg.KalmanR*s.cfg.KalmanR)
		kVelPct = vel / price
	}

	n := len(s.prices)
	if n < s.warmupNeed {
		fmt.Printf("[%s] [%-5s] 数据采集中 $%.2f (%d/%d)\n",
			time.Now().Format("15:04:05"), s.cfg.Name, price, n, s.warmupNeed)
		return
	}

	// ─── 滚动窗口分析 ───
	window := s.cfg.DCBDropPeriod
	if n < window {
		window = n
	}
	wp := s.prices[n-window:]

	// 找窗口内最低点位置
	lowIdx := 0
	localLow := wp[0]
	for i, p := range wp {
		if p < localLow {
			localLow = p
			lowIdx = i
		}
	}

	// 低点之前的最高点（下跌起始）
	dropHigh := wp[0]
	for _, p := range wp[:lowIdx+1] {
		if p > dropHigh {
			dropHigh = p
		}
	}

	// 低点之后的最高点（反弹高点，含当前价）
	bounceHigh := localLow
	for _, p := range wp[lowIdx:] {
		if p > bounceHigh {
			bounceHigh = p
		}
	}

	totalDrop := dropHigh - localLow
	bounceSize := bounceHigh - localLow
	dropPct := 0.0
	if dropHigh > 0 {
		dropPct = totalDrop / dropHigh
	}
	bounceRatio := 0.0
	if totalDrop > 0 {
		bounceRatio = bounceSize / totalDrop
	}

	// 更新连续下跌计数
	if n >= 2 && price < s.prices[n-2] {
		s.dcbConsecutiveDown++
	} else {
		s.dcbConsecutiveDown = 0
	}

	// ─── 指标状态字符串 ───
	kColor := "\033[33m"
	if kVelPct > 0 {
		kColor = "\033[32m"
	} else if kVelPct < 0 {
		kColor = "\033[31m"
	}
	patternTag := fmt.Sprintf("跌%.3f%%(需≥%.1f%%) 反弹%.1f%%(需%.1f%%~%.0f%%)",
		dropPct*100, s.cfg.DCBDropMinPct*100,
		bounceRatio*100, s.cfg.DCBBounceMinPct/localLow*100*1000, s.cfg.DCBBounceMaxPct*100)
	indicators := fmt.Sprintf("$%.2f K:%s%+.4f%%\033[0m 窗口:%s",
		price, kColor, kVelPct*100, patternTag)

	signal := ""
	equity := s.p.totalEquity(s.cfg, price)
	if equity > s.p.peakEquity {
		s.p.peakEquity = equity
	}

	// ─── 优先级 1：平仓（止损 / 止盈 / 超时）───
	if s.p.inPosition() {
		pct := s.p.positionPct(price)
		holdSec := time.Since(s.openTime).Seconds()
		switch {
		case pct <= -s.cfg.StopLoss:
			s.p.closePos(s.cfg, price, "止损", &s.trades)
			signal = fmt.Sprintf("\033[31m止损(%.3f%%)\033[0m", pct*100)
		case pct >= s.cfg.TakeProfit:
			s.p.closePos(s.cfg, price, "止盈", &s.trades)
			signal = fmt.Sprintf("\033[32m止盈(+%.3f%%)\033[0m", pct*100)
		case s.cfg.DCBMaxHoldSec > 0 && holdSec >= float64(s.cfg.DCBMaxHoldSec):
			s.p.closePos(s.cfg, price, "超时", &s.trades)
			signal = fmt.Sprintf("超时强平(持%.0fs %+.3f%%)", holdSec, pct*100)
		}
	}

	// ─── 优先级 2：死猫做空入场 ───
	isDrop := lowIdx > 0 && dropPct >= s.cfg.DCBDropMinPct
	isBounce := bounceSize/localLow >= s.cfg.DCBBounceMinPct
	isWeakBounce := bounceRatio <= s.cfg.DCBBounceMaxPct
	isPriceBelow := price < bounceHigh
	kVelOK := s.cfg.KalmanVelThresh <= 0 || kVelPct < -s.cfg.KalmanVelThresh
	inCooldown := time.Since(s.p.lastTradeTime) < s.cfg.tradeCooldown()

	if !s.p.inPosition() && !inCooldown && signal == "" &&
		isDrop && isBounce && isWeakBounce && isPriceBelow &&
		s.dcbConsecutiveDown >= s.cfg.DCBConfirmTicks && kVelOK {
		s.p.openPos(s.cfg, dirShort, price, &s.trades)
		s.openTime = time.Now()
		signal = fmt.Sprintf("\033[31m死猫做空\033[0m 跌%.3f%% 反弹%.1f%%(≤%.0f%%) 确认%d格",
			dropPct*100, bounceRatio*100, s.cfg.DCBBounceMaxPct*100, s.dcbConsecutiveDown)
	}

	// ─── 无信号时显示等待/持仓状态 ───
	ck := func(ok bool) string {
		if ok {
			return "\033[32m✓\033[0m"
		}
		return "\033[31m✗\033[0m"
	}
	if signal == "" {
		if !s.p.inPosition() {
			signal = fmt.Sprintf("等死猫 跌%s 反弹%s 弱%s 确认%d/%d格%s K%s 冷却%s",
				ck(isDrop), ck(isBounce), ck(isWeakBounce),
				s.dcbConsecutiveDown, s.cfg.DCBConfirmTicks, ck(s.dcbConsecutiveDown >= s.cfg.DCBConfirmTicks),
				ck(kVelOK), ck(!inCooldown))
		} else {
			pct := s.p.positionPct(price)
			holdSec := time.Since(s.openTime).Seconds()
			slPrice := s.p.entryPrice * (1 + s.cfg.StopLoss)
			tpPrice := s.p.entryPrice * (1 - s.cfg.TakeProfit)
			signal = fmt.Sprintf("持空头 止损$%.0f(差$%.0f) 止盈$%.0f(差$%.0f) 已持%.0fs/%ds 当前%+.3f%%",
				slPrice, slPrice-price, tpPrice, price-tpPrice,
				holdSec, s.cfg.DCBMaxHoldSec, pct*100)
		}
	}

	s.printStatus(price, endTime, indicators, signal)
}
