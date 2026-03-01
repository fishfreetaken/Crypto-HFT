package tradelib

import (
	"fmt"
	"math"
	"time"
)

// onPriceLiqTrap 流动性陷阱策略（博弈论框架）
//
// ══ 博弈论原理 ══════════════════════════════════════════════════════════════
//
//  市场是一个重复博弈，参与者分为：
//    • 散户（可预测）：在技术高低点附近密集挂止损和追突破买卖单
//    • 机构/做市商（主动方）：拥有订单簿信息，能感知止损密集区
//
//  机构行为——"止损猎杀"（Stop Hunt）：
//    1. 推价至止损密集区（N周期高点上方 / 低点下方）
//    2. 触发大量被动订单（多头止损卖单 + 追涨买单）
//    3. 吸收这批流动性后，失去推价动力，价格急速回归
//
//  纳什均值：被猎杀后，该方向的"弱手"已被清洗，
//            市场短期内往往向反方向运动（弱手的反向积累）
//
// ══ 信号逻辑 ═══════════════════════════════════════════════════════════════
//
//  上方扫荡（Up Sweep）→ 做空：
//    • 价格短暂突破 N周期高点 ≥ SweepPct → 多头止损 & 追涨买单被吃
//    • 当前价格已回归至高点以下，且连续 ConfirmTicks 个 tick 下跌
//    • Kalman 速度方向向下确认（可选）
//    → 入场做空，止损设在扫荡极值上方
//
//  下方扫荡（Down Sweep）→ 做多：
//    • 价格短暂跌破 N周期低点 ≥ SweepPct → 空头止损 & 追跌卖单被吃
//    • 当前价格已回归至低点以上，且连续 ConfirmTicks 个 tick 上涨
//    • Kalman 速度方向向上确认（可选）
//    → 入场做多，止损设在扫荡极值下方
//
// ══ 参数建议（30s采样） ═════════════════════════════════════════════════════
//   lh_trap_window       = 80     （约40分钟高低点结构）
//   lh_trap_sweep_pct    = 0.0015 （突破0.15%确认为扫荡而非真突破）
//   lh_trap_confirm_ticks = 2     （2格回归tick避免过早入场）
//   lh_trap_max_hold_sec = 600    （10分钟超时强平）
//   stop_loss            = 0.008  （止损设在扫荡极值外，约0.8%）
//   take_profit          = 0.020  （目标对侧结构，约2%）
//   leverage             = 6      （风险收益比 ≈ 2.5:1 @ 6x）

func (s *Strategy) onPriceLiqTrap(tick Tick, endTime time.Time) {
	price := tick.Prc
	s.feedPrice(price)

	// ─── Kalman 滤波 ───
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

	// ─── 划分价格窗口 ───────────────────────────────────────────────────────
	//
	//  priceBuf 存储顺序：Get(0)=当前, Get(1)=上一tick, ...
	//
	//  sweepSearch: 最近几个 tick 内寻找"扫荡尖刺"
	//               排除 Get(0)（当前 tick），检查 Get(1..sweepSearch)
	//  baseZone:    sweepSearch+1 之后的历史区间，用于确定"真实结构高低点"
	//               (即扫荡发生之前的市场结构)

	const sweepSearch = 15 // 最近 15 个 tick 内找尖刺
	baseEnd := s.Cfg.LHTrapWindow
	if n < baseEnd {
		baseEnd = n
	}
	baseStart := sweepSearch + 1
	if baseStart >= baseEnd {
		baseStart = baseEnd / 2
	}

	// 基准高低（历史结构区）
	baseHigh, baseLow := 0.0, math.MaxFloat64
	for i := baseStart; i < baseEnd; i++ {
		p := s.priceBuf.Get(i)
		if p > baseHigh {
			baseHigh = p
		}
		if p < baseLow {
			baseLow = p
		}
	}

	// 近期尖刺高低（排除当前 tick，只看回溯 1..sweepSearch）
	recentHigh, recentLow := 0.0, math.MaxFloat64
	for i := 1; i <= sweepSearch && i < n; i++ {
		p := s.priceBuf.Get(i)
		if p > recentHigh {
			recentHigh = p
		}
		if p < recentLow {
			recentLow = p
		}
	}

	// ─── 扫荡判断 ───────────────────────────────────────────────────────────
	//  upSwept:  近期尖刺高点 ≥ 基准高点×(1+sweepPct)，且当前已回归基准高点以下
	//  dnSwept:  近期尖刺低点 ≤ 基准低点×(1-sweepPct)，且当前已回归基准低点以上
	upSwept := baseHigh > 0 &&
		recentHigh >= baseHigh*(1+s.Cfg.LHTrapSweepPct) &&
		price < baseHigh

	dnSwept := baseLow < math.MaxFloat64 &&
		recentLow <= baseLow*(1-s.Cfg.LHTrapSweepPct) &&
		price > baseLow

	// ─── 连续 tick 方向计数 ─────────────────────────────────────────────────
	if n >= 2 {
		prev := s.priceBuf.Get(1)
		if price < prev {
			s.trapConsecDown++
			s.trapConsecUp = 0
		} else if price > prev {
			s.trapConsecUp++
			s.trapConsecDown = 0
		}
	}

	// ─── Kalman 方向确认 ────────────────────────────────────────────────────
	//  KalmanVelThresh=0：不限速，只看方向（速度<0=空，速度>0=多）
	//  KalmanVelThresh>0：速度绝对值须超过阈值才确认
	kShortOK := s.Cfg.KalmanR <= 0 || !s.kf.ready ||
		kVelPct <= -s.Cfg.KalmanVelThresh
	kLongOK := s.Cfg.KalmanR <= 0 || !s.kf.ready ||
		kVelPct >= s.Cfg.KalmanVelThresh

	// ─── 权益 / 峰值更新 ────────────────────────────────────────────────────
	equity := s.p.totalEquity(s.Cfg, tick)
	if equity > s.p.peakEquity {
		s.p.peakEquity = equity
	}

	signal := ""

	// ─── 优先级 1：平仓 ─────────────────────────────────────────────────────
	if s.p.inPosition() {
		triggered, stopSignal := s.p.checkStops(s.Cfg, tick, &s.trades)
		if triggered {
			signal = stopSignal
		} else {
			holdSec := time.Since(s.openTime).Seconds()
			if s.Cfg.LHTrapMaxHoldSec > 0 && holdSec >= float64(s.Cfg.LHTrapMaxHoldSec) {
				s.p.closePos(s.Cfg, tick, "超时", &s.trades)
				signal = fmt.Sprintf("超时强平(持%.0fs)", holdSec)
			}
		}
	}

	// ─── 优先级 2：入场 ─────────────────────────────────────────────────────
	inCooldown := time.Since(s.p.lastTradeTime) < s.Cfg.tradeCooldown()

	if !s.p.inPosition() && !inCooldown && signal == "" {
		switch {
		case upSwept && s.trapConsecDown >= s.Cfg.LHTrapConfirmTicks && kShortOK:
			// 上方扫荡完成，弱多已被清洗 → 做空
			s.p.openPosVolAdjusted(s.Cfg, dirShort, tick, 0.015, &s.trades)
			s.openTime = time.Now()
			signal = fmt.Sprintf(
				"\033[31m陷阱做空\033[0m 高$%.0f→扫至$%.0f(+%.2f%%) 回归↓%d格 K:%+.4f%%",
				baseHigh, recentHigh, (recentHigh/baseHigh-1)*100,
				s.trapConsecDown, kVelPct*100)

		case dnSwept && s.trapConsecUp >= s.Cfg.LHTrapConfirmTicks && kLongOK:
			// 下方扫荡完成，弱空已被清洗 → 做多
			s.p.openPosVolAdjusted(s.Cfg, dirLong, tick, 0.015, &s.trades)
			s.openTime = time.Now()
			signal = fmt.Sprintf(
				"\033[32m陷阱做多\033[0m 低$%.0f→扫至$%.0f(-%.2f%%) 回归↑%d格 K:%+.4f%%",
				baseLow, recentLow, (1-recentLow/baseLow)*100,
				s.trapConsecUp, kVelPct*100)
		}
	}

	// ─── 指标字符串 ─────────────────────────────────────────────────────────
	kColor := "\033[33m"
	if kVelPct > 0 {
		kColor = "\033[32m"
	} else if kVelPct < 0 {
		kColor = "\033[31m"
	}
	sweepTag := "──"
	if upSwept {
		sweepTag = fmt.Sprintf("\033[31m↑扫(%.2f%%)\033[0m", (recentHigh/baseHigh-1)*100)
	} else if dnSwept {
		sweepTag = fmt.Sprintf("\033[32m↓扫(%.2f%%)\033[0m", (1-recentLow/baseLow)*100)
	}
	indicators := fmt.Sprintf("$%.2f K:%s%+.4f%%\033[0m %s 结构H:$%.0f L:$%.0f",
		price, kColor, kVelPct*100, sweepTag, baseHigh, baseLow)

	// ─── 无信号时填充监控状态（Quiet 模式下不填充，printStatus 会静默） ─────
	if signal == "" && !s.Cfg.Quiet {
		ck := func(ok bool) string {
			if ok {
				return "\033[32m✓\033[0m"
			}
			return "\033[31m✗\033[0m"
		}
		if !s.p.inPosition() {
			signal = fmt.Sprintf(
				"监控 上扫%s(↓%d/%d格)%s 下扫%s(↑%d/%d格)%s 冷却%s",
				ck(upSwept), s.trapConsecDown, s.Cfg.LHTrapConfirmTicks, ck(kShortOK),
				ck(dnSwept), s.trapConsecUp, s.Cfg.LHTrapConfirmTicks, ck(kLongOK),
				ck(!inCooldown))
		} else {
			pct := s.p.positionPct(tick)
			holdSec := time.Since(s.openTime).Seconds()
			var slPrice, tpPrice float64
			if s.p.direction == dirLong {
				slPrice = s.p.entryPrice * (1 - s.Cfg.StopLoss)
				tpPrice = s.p.entryPrice * (1 + s.Cfg.TakeProfit)
				signal = fmt.Sprintf("持多 止损$%.0f(差$%.0f) 止盈$%.0f(差$%.0f) 持%.0fs/%ds 当前%+.3f%%",
					slPrice, price-slPrice, tpPrice, tpPrice-price,
					holdSec, s.Cfg.LHTrapMaxHoldSec, pct*100)
			} else {
				slPrice = s.p.entryPrice * (1 + s.Cfg.StopLoss)
				tpPrice = s.p.entryPrice * (1 - s.Cfg.TakeProfit)
				signal = fmt.Sprintf("持空 止损$%.0f(差$%.0f) 止盈$%.0f(差$%.0f) 持%.0fs/%ds 当前%+.3f%%",
					slPrice, slPrice-price, tpPrice, price-tpPrice,
					holdSec, s.Cfg.LHTrapMaxHoldSec, pct*100)
			}
		}
	}

	s.printStatus(tick, endTime, indicators, signal)
}
