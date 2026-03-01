package tradelib

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
func (s *Strategy) onPriceWaterfall(tick Tick, endTime time.Time) {
	price := tick.Prc
	s.feedPrice(price)

	// Kalman 滤波
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
			fmt.Printf("[%s] [%-5s] 数据采集中 $%.2f (%d/%d)\n",
				time.Now().Format("15:04:05"), s.Cfg.Name, price, n, s.warmupNeed)
		}
		return
	}

	// ─── 连续下跌 + 跌速检测 ───
	window := s.Cfg.WFConsecutiveTicks + 1
	allDown := false
	avgTickDropPct := 0.0
	consecutiveCount := 0
	if n >= window {
		recent := make([]float64, window)
		for i := 0; i < window; i++ {
			recent[window-1-i] = s.priceBuf.Get(i)
		}
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
			avgTickDropPct = totalDrop / float64(s.Cfg.WFConsecutiveTicks) / price
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
	velTag := fmt.Sprintf("%.3f%%/格(需≥%.3f%%)", avgTickDropPct*100, s.Cfg.WFMinVelPct*100)
	indicators := fmt.Sprintf("$%.2f K:%s%+.4f%%\033[0m %s 均速:%s",
		price, kColor, kVelPct*100, downTag, velTag)

	signal := ""
	equity := s.p.totalEquity(s.Cfg, tick)
	if equity > s.p.peakEquity {
		s.p.peakEquity = equity
	}

	// ─── 优先级 1：平仓（止损 / 止盈 / 跟踪止损 / 超时）───
	if s.p.inPosition() {
		triggered, stopSignal := s.p.checkStops(s.Cfg, tick, &s.trades)
		if triggered {
			signal = stopSignal
		} else {
			holdSec := time.Since(s.openTime).Seconds()
			if s.Cfg.WFMaxHoldSec > 0 && holdSec >= float64(s.Cfg.WFMaxHoldSec) {
				s.p.closePos(s.Cfg, tick, "超时", &s.trades)
				pct := s.p.positionPct(tick)
				signal = fmt.Sprintf("超时强平(持%.0fs %+.3f%%)", holdSec, pct*100)
			}
		}
	}

	// ─── 优先级 2：瀑布做空入场 ───
	kVelOK := s.Cfg.KalmanVelThresh <= 0 || kVelPct < -s.Cfg.KalmanVelThresh
	velOK := allDown && avgTickDropPct >= s.Cfg.WFMinVelPct
	inCooldown := time.Since(s.p.lastTradeTime) < s.Cfg.tradeCooldown()
	
	obiOK := true
	if tick.AskVol > 0 && tick.BidVol > 0 {
		obiOK = tick.AskVol > tick.BidVol*1.1 // 卖盘压制买盘至少 10%
	}

	if !s.p.inPosition() && !inCooldown && signal == "" && velOK && kVelOK && obiOK {
		s.p.openPosVolAdjusted(s.Cfg, dirShort, tick, 0.015, &s.trades) // avg volatility assumed
		s.openTime = time.Now()
		signal = fmt.Sprintf("\033[31m瀑布做空\033[0m 连跌%d格 均速%.3f%%/格 K:%+.4f%% OBI:%.1fx",
			consecutiveCount, avgTickDropPct*100, kVelPct*100, tick.AskVol/(tick.BidVol+0.0001))
	}

	// ─── 无信号时显示状态 ───
	ck := func(ok bool) string {
		if ok {
			return "\033[32m✓\033[0m"
		}
		return "\033[31m✗\033[0m"
	}
	if signal == "" && !s.Cfg.Quiet {
		if !s.p.inPosition() {
			signal = fmt.Sprintf("监控瀑布 连跌%s 均速%s K%s OBI%s 冷却%s",
				ck(allDown), ck(allDown && avgTickDropPct >= s.Cfg.WFMinVelPct),
				ck(kVelOK), ck(obiOK), ck(!inCooldown))
		} else {
			pct := s.p.positionPct(tick)
			holdSec := time.Since(s.openTime).Seconds()
			slPrice := s.p.entryPrice * (1 + s.Cfg.StopLoss)
			tpPrice := s.p.entryPrice * (1 - s.Cfg.TakeProfit)
			signal = fmt.Sprintf("持空头 止损$%.0f(差$%.0f) 止盈$%.0f(差$%.0f) 已持%.0fs/%ds 当前%+.3f%%",
				slPrice, slPrice-price, tpPrice, price-tpPrice,
				holdSec, s.Cfg.WFMaxHoldSec, pct*100)
		}
	}

	s.printStatus(tick, endTime, indicators, signal)
}
