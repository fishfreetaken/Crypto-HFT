package main

import (
	"fmt"
	"time"
)

// onPriceWaterfall 瀑布加速策略：
//
// 原理（止损连锁→级联下跌）：
//   - 当连续 WFConsecutiveTicks 个 tick 均下跌，且平均跌速 ≥ WFMinVelPct/tick
//   - 说明多头止损被批量触发形成级联，趋势短期内强烈持续
//   - Kalman 速度方向确认后高杠杆追空，快进快出（WFMaxHoldSec 超时平仓）
func (s *Strategy) onPriceWaterfall(price float64, endTime time.Time) {
	s.prices = append(s.prices, price)

	// Kalman 滤波
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

	// ─── 连续下跌 + 跌速检测 ───
	window := s.cfg.WFConsecutiveTicks + 1
	allDown := false
	avgTickDropPct := 0.0
	consecutiveCount := 0
	if n >= window {
		recent := s.prices[n-window:]
		allDown = true
		totalDrop := 0.0
		for i := 1; i < len(recent); i++ {
			delta := recent[i] - recent[i-1]
			if delta >= 0 {
				allDown = false
				break
			}
			totalDrop += -delta
			consecutiveCount++
		}
		if allDown {
			avgTickDropPct = totalDrop / float64(s.cfg.WFConsecutiveTicks) / price
		}
	}

	// ─── 指标状态字符串 ───
	kColor := "\033[33m"
	if kVelPct > 0 {
		kColor = "\033[32m"
	} else if kVelPct < 0 {
		kColor = "\033[31m"
	}
	downTag := "\033[31m✗\033[0m"
	if allDown {
		downTag = fmt.Sprintf("\033[32m连跌%d格\033[0m", consecutiveCount)
	}
	velTag := fmt.Sprintf("%.3f%%/格(需≥%.3f%%)", avgTickDropPct*100, s.cfg.WFMinVelPct*100)
	indicators := fmt.Sprintf("$%.2f K:%s%+.4f%%\033[0m %s 均速:%s",
		price, kColor, kVelPct*100, downTag, velTag)

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
		case s.cfg.WFMaxHoldSec > 0 && holdSec >= float64(s.cfg.WFMaxHoldSec):
			s.p.closePos(s.cfg, price, "超时", &s.trades)
			signal = fmt.Sprintf("超时强平(持%.0fs %+.3f%%)", holdSec, pct*100)
		}
	}

	// ─── 优先级 2：瀑布做空入场 ───
	kVelOK := s.cfg.KalmanVelThresh <= 0 || kVelPct < -s.cfg.KalmanVelThresh
	velOK := allDown && avgTickDropPct >= s.cfg.WFMinVelPct
	inCooldown := time.Since(s.p.lastTradeTime) < s.cfg.tradeCooldown()

	if !s.p.inPosition() && !inCooldown && signal == "" && velOK && kVelOK {
		s.p.openPos(s.cfg, dirShort, price, &s.trades)
		s.openTime = time.Now()
		signal = fmt.Sprintf("\033[31m瀑布做空\033[0m 连跌%d格 均速%.3f%%/格 K:%+.4f%%",
			consecutiveCount, avgTickDropPct*100, kVelPct*100)
	}

	// ─── 无信号时显示状态 ───
	ck := func(ok bool) string {
		if ok {
			return "\033[32m✓\033[0m"
		}
		return "\033[31m✗\033[0m"
	}
	if signal == "" {
		if !s.p.inPosition() {
			signal = fmt.Sprintf("监控瀑布 连跌%s 均速%s K%s 冷却%s",
				ck(allDown), ck(allDown && avgTickDropPct >= s.cfg.WFMinVelPct),
				ck(kVelOK), ck(!inCooldown))
		} else {
			pct := s.p.positionPct(price)
			holdSec := time.Since(s.openTime).Seconds()
			slPrice := s.p.entryPrice * (1 + s.cfg.StopLoss)
			tpPrice := s.p.entryPrice * (1 - s.cfg.TakeProfit)
			signal = fmt.Sprintf("持空头 止损$%.0f(差$%.0f) 止盈$%.0f(差$%.0f) 已持%.0fs/%ds 当前%+.3f%%",
				slPrice, slPrice-price, tpPrice, price-tpPrice,
				holdSec, s.cfg.WFMaxHoldSec, pct*100)
		}
	}

	s.printStatus(price, endTime, indicators, signal)
}
