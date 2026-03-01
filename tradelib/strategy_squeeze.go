package tradelib

import (
	"fmt"
	"math"
	"time"
)

// onPriceSqueezeBreakout 挤压突破策略：
//
// 原理（情绪盘整→释放）：
//   - 布林带（BB）衡量短期价格波动范围；Keltner 通道（KC = EMA ± mult×ATR）衡量趋势振幅
//   - 当 BB 被 KC"吞入"（BB upper < KC upper 且 BB lower > KC lower）→ 挤压状态：市场盘整蓄力
//   - 当 BB 突破 KC（任意一侧）→ 挤压释放：情绪突变，往往伴随快速单边行情
//   - 在突破触发的第一个 tick 立即以高杠杆入场，方向由价格相对 BB 中轨决定
//   - 结合 ER（效率比率）过滤假突破（ER 低 = 突破后很快反转 = 假）
//   - 设置超时强平（持仓超过 squeeze_max_hold_sec 秒则强制出场，防止突破衰减变震荡）
func (s *Strategy) onPriceSqueezeBreakout(tick Tick, endTime time.Time) {
	price := tick.Prc
	s.feedPrice(price)

	// Kalman 滤波（预热期也运行，积累状态估计）
	kVelPct := 0.0
	if s.Cfg.KalmanR > 0 {
		p2 := price * price
		_, vel := s.kf.step(price,
			p2*s.Cfg.KalmanQPos*s.Cfg.KalmanQPos,
			p2*s.Cfg.KalmanQVel*s.Cfg.KalmanQVel,
			p2*s.Cfg.KalmanR*s.Cfg.KalmanR)
		kVelPct = vel / price
	}

	if s.priceBuf.count < s.warmupNeed {
		if !s.Cfg.Quiet {
			fmt.Printf("[%s] [%-5s] 数据采集中 $%.2f (%d/%d)\n",
				time.Now().Format("15:04:05"), s.Cfg.Name, price, s.priceBuf.count, s.warmupNeed)
		}
		return
	}

	// ─── 计算 BB + Keltner 通道 ───
	bbUpper, bbMid, bbLower := calcBollingerBands(s.priceBuf, s.Cfg.SqueezeBBPeriod, s.Cfg.SqueezeBBStdDev)
	atr := calcATR(s.priceBuf, s.Cfg.SqueezeATRPeriod)
	kcEMA := s.emaKC.Value()
	kcUpper := kcEMA + s.Cfg.SqueezeKCMult*atr
	kcLower := kcEMA - s.Cfg.SqueezeKCMult*atr
	er := calcER(s.priceBuf, s.Cfg.SqueezeATRPeriod)

	// BB 宽度（百分比）
	bbWidth := 0.0
	if bbMid > 0 {
		bbWidth = (bbUpper - bbLower) / bbMid * 100
	}
	kcWidth := 0.0
	compressionPct := 0.0
	if kcEMA > 0 {
		kcWidth = (kcUpper - kcLower) / kcEMA * 100
	}
	if kcWidth > 0 {
		compressionPct = math.Max(0, (1-bbWidth/kcWidth)*100)
	}

	// 挤压检测：
	//   SqueezeBBWidthPct > 0 → 用 BB 宽度绝对阈值（适合 5s 采样，ATR 极小导致 KC 极窄）
	//   SqueezeBBWidthPct = 0 → 经典 BB⊂KC 判法（适合 30s+ 采样）
	var inSqueeze bool
	if s.Cfg.SqueezeBBWidthPct > 0 {
		bbWidthRatio := 0.0
		if bbMid > 0 {
			bbWidthRatio = (bbUpper - bbLower) / bbMid
		}
		inSqueeze = bbMid > 0 && bbWidthRatio < s.Cfg.SqueezeBBWidthPct
	} else {
		inSqueeze = bbUpper < kcUpper && bbLower > kcLower
	}
	// 突破 = 上一 tick 挤压，本 tick 突破（捕捉第一个释放 tick）
	isBreakout := s.prevInSqueeze && !inSqueeze

	// 构建指标状态字符串
	squeezeTag := "自由"
	if inSqueeze {
		if s.Cfg.SqueezeBBWidthPct > 0 {
			squeezeTag = fmt.Sprintf("\033[33m挤压中(BB%.4f%%<%.4f%%)\\033[0m",
				bbWidth, s.Cfg.SqueezeBBWidthPct*100)
		} else {
			squeezeTag = fmt.Sprintf("\033[33m挤压中(压缩%.0f%%)\033[0m", compressionPct)
		}
	} else if isBreakout {
		squeezeTag = "\033[32m突破!\033[0m"
	}
	kColor := "\033[33m"
	if kVelPct > 0 {
		kColor = "\033[32m"
	} else if kVelPct < 0 {
		kColor = "\033[31m"
	}
	indicators := fmt.Sprintf("$%.2f BB宽:%.3f%% KC宽:%.3f%% ER:%.2f K:%s%+.4f%%\033[0m %s",
		price, bbWidth, kcWidth, er, kColor, kVelPct*100, squeezeTag)

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
			if s.Cfg.SqueezeMaxHoldSec > 0 && holdSec >= float64(s.Cfg.SqueezeMaxHoldSec) {
				s.p.closePos(s.Cfg, tick, "超时", &s.trades)
				pct := s.p.positionPct(tick)
				signal = fmt.Sprintf("超时强平(持%.0fs %+.3f%%)", holdSec, pct*100)
			}
		}
	}

	// ─── 优先级 2：开仓（仅在突破触发时入场）───
	kVelLongOK := s.Cfg.KalmanVelThresh <= 0 || kVelPct > s.Cfg.KalmanVelThresh
	kVelShortOK := s.Cfg.KalmanVelThresh <= 0 || kVelPct < -s.Cfg.KalmanVelThresh
	inCooldown := time.Since(s.p.lastTradeTime) < s.Cfg.tradeCooldown()
	if !s.p.inPosition() && !inCooldown && isBreakout && er > s.Cfg.SqueezeConfirmER && signal == "" {
		if price > bbMid && kVelLongOK { // 向上突破 + Kalman 速度向上确认 → 做多
			s.p.openPosVolAdjusted(s.Cfg, dirLong, tick, 0.015, &s.trades) // avg volatility assumed
			s.openTime = time.Now()
			signal = fmt.Sprintf("\033[32m挤压突破做多\033[0m (BB:%.3f%%→KC:%.3f%% ER:%.2f K:%+.4f%%)",
				bbWidth, kcWidth, er, kVelPct*100)
		} else if price <= bbMid && kVelShortOK { // 向下突破 + Kalman 速度向下确认 → 做空
			s.p.openPosVolAdjusted(s.Cfg, dirShort, tick, 0.015, &s.trades) // avg volatility assumed
			s.openTime = time.Now()
			signal = fmt.Sprintf("\033[31m挤压突破做空\033[0m (BB:%.3f%%→KC:%.3f%% ER:%.2f K:%+.4f%%)",
				bbWidth, kcWidth, er, kVelPct*100)
		}
	}

	s.prevInSqueeze = inSqueeze

	// 无信号时显示下一触发条件
	ck := func(ok bool) string {
		if ok { return "\033[32m✓\033[0m" }
		return "\033[31m✗\033[0m"
	}
	if signal == "" && !s.Cfg.Quiet {
		if !s.p.inPosition() {
			kDir := "↑多↓空"
			if s.Cfg.KalmanVelThresh > 0 {
				if kVelPct > s.Cfg.KalmanVelThresh {
					kDir = "\033[32m↑做多\033[0m"
				} else if kVelPct < -s.Cfg.KalmanVelThresh {
					kDir = "\033[31m↓做空\033[0m"
				} else {
					kDir = "\033[33m游走待确认\033[0m"
				}
			}
			signal = fmt.Sprintf("等突破 挤压%s ER:%.2f>%.2f%s K方向:%s 冷却%s",
				ck(inSqueeze), er, s.Cfg.SqueezeConfirmER, ck(er > s.Cfg.SqueezeConfirmER),
				kDir, ck(!inCooldown))
		} else {
			pct := s.p.positionPct(tick)
			holdSec := time.Since(s.openTime).Seconds()
			var slPrice, tpPrice float64
			if s.p.direction == dirLong {
				slPrice = s.p.entryPrice * (1 - s.Cfg.StopLoss)
				tpPrice = s.p.entryPrice * (1 + s.Cfg.TakeProfit)
				signal = fmt.Sprintf("持多 止损$%.0f(差$%.0f) 止盈$%.0f(差$%.0f) 已持%.0fs/%ds 当前%+.3f%%",
					slPrice, price-slPrice, tpPrice, tpPrice-price,
					holdSec, s.Cfg.SqueezeMaxHoldSec, pct*100)
			} else {
				slPrice = s.p.entryPrice * (1 + s.Cfg.StopLoss)
				tpPrice = s.p.entryPrice * (1 - s.Cfg.TakeProfit)
				signal = fmt.Sprintf("持空 止损$%.0f(差$%.0f) 止盈$%.0f(差$%.0f) 已持%.0fs/%ds 当前%+.3f%%",
					slPrice, slPrice-price, tpPrice, price-tpPrice,
					holdSec, s.Cfg.SqueezeMaxHoldSec, pct*100)
			}
		}
	}

	s.printStatus(tick, endTime, indicators, signal)
}
