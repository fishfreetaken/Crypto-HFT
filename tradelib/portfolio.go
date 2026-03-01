package tradelib

import (
	"fmt"
	"math"
	"time"
)

// ===== 仓位方向 =====

type posDir int8

const (
	dirNone  posDir = 0
	dirLong  posDir = 1
	dirShort posDir = -1
)

func (d posDir) String() string {
	switch d {
	case dirLong:
		return "多"
	case dirShort:
		return "空头"
	default:
		return "无"
	}
}

// ===== 投资组合 =====

type tradeRecord struct {
	ts     time.Time
	action string
	price  float64
	equity float64
}

type portfolio struct {
	name          string // 策略名，用于日志前缀
	cash          float64
	margin        float64
	direction     posDir
	entryPrice    float64
	extremePrice  float64 // 持多单时记期间最高价，持空头时记期间最低价
	dynamicLev    float64 // 本次开仓时的动态杠杆倍数
	lastTradeTime time.Time
	longCount     int
	shortCount    int
	closeCount    int
	peakEquity    float64
}

func (p *portfolio) inPosition() bool {
	return p.direction != dirNone
}

func (p *portfolio) totalEquity(cfg Config, tick Tick) float64 {
	if !p.inPosition() {
		return p.cash
	}
	currentExitPrice := tick.Bid1
	if p.direction == dirShort {
		currentExitPrice = tick.Ask1
	}
	pricePct := (currentExitPrice - p.entryPrice) / p.entryPrice * float64(p.direction)
	return p.margin * (1 + p.dynamicLev*pricePct)
}

func (p *portfolio) positionPct(tick Tick) float64 {
	if p.entryPrice == 0 {
		return 0
	}
	currentExitPrice := tick.Bid1
	if p.direction == dirShort {
		currentExitPrice = tick.Ask1
	}
	return (currentExitPrice - p.entryPrice) / p.entryPrice * float64(p.direction)
}

func (p *portfolio) openPos(cfg Config, d posDir, tick Tick, trades *[]tradeRecord) {
	p.openPosVolAdjusted(cfg, d, tick, 0.015, trades)
}

func (p *portfolio) openPosVolAdjusted(cfg Config, d posDir, tick Tick, volPct float64, trades *[]tradeRecord) {
	entryPrice := tick.Ask1
	if d == dirShort {
		entryPrice = tick.Bid1
	}

	actualLeverage := cfg.Leverage
	if volPct > 0 {
		baseVol := 0.015
		adj := baseVol / volPct
		if adj > 2.0 {
			adj = 2.0
		} else if adj < 0.2 {
			adj = 0.2
		}
		actualLeverage = cfg.Leverage * adj
	}

	openFee := p.cash * actualLeverage * cfg.TradeFee
	p.margin = p.cash - openFee
	p.cash = 0
	p.direction = d
	p.entryPrice = entryPrice
	p.extremePrice = entryPrice
	p.dynamicLev = actualLeverage
	p.lastTradeTime = time.Now()

	action := "做多"
	if d == dirLong {
		p.longCount++
	} else {
		action = "做空"
		p.shortCount++
	}
	*trades = append(*trades, tradeRecord{
		ts: time.Now(), action: action, price: entryPrice, equity: p.margin,
	})
	fmt.Printf("[%s] [%-4s] \033[36m%s\033[0m @ $%.2f | 保证金:$%.2f 动态杠杆:%.1fx (波幅:%.2f%%)\n",
		time.Now().Format("15:04:05"), p.name, action, entryPrice, p.margin, actualLeverage, volPct*100)
}

func (p *portfolio) closePos(cfg Config, tick Tick, reason string, trades *[]tradeRecord) {
	exitPrice := tick.Bid1
	if p.direction == dirShort {
		exitPrice = tick.Ask1
	}
	pricePct := p.positionPct(tick)
	closeFee := p.margin * p.dynamicLev * cfg.TradeFee
	newCash := p.margin*(1+p.dynamicLev*pricePct) - closeFee
	if newCash < 0 {
		newCash = 0
	}
	equityChangePct := (newCash/p.margin - 1) * 100

	fmt.Printf("[%s] [%-4s] \033[33m平仓(%s)\033[0m @ $%.2f | %s仓 | 价格:%+.3f%% | $%.2f→$%.2f(%+.2f%%)\n",
		time.Now().Format("15:04:05"), p.name, reason, exitPrice, p.direction,
		pricePct*100, p.margin, newCash, equityChangePct)

	*trades = append(*trades, tradeRecord{
		ts: time.Now(), action: "平仓(" + reason + ")", price: exitPrice, equity: newCash,
	})
	p.closeCount++
	p.lastTradeTime = time.Now()
	p.cash = newCash
	p.margin = 0
	p.direction = dirNone
	p.entryPrice = 0
	p.extremePrice = 0
	p.dynamicLev = 0
}

func (p *portfolio) checkStops(cfg Config, tick Tick, trades *[]tradeRecord) (triggered bool, signal string) {
	if !p.inPosition() {
		return false, ""
	}
	pct := p.positionPct(tick)

	if p.direction == dirLong {
		if tick.Bid1 > p.extremePrice || p.extremePrice == 0 {
			p.extremePrice = tick.Bid1
		}
	} else {
		if tick.Ask1 < p.extremePrice || p.extremePrice == 0 {
			p.extremePrice = tick.Ask1
		}
	}

	if cfg.TrailingStop > 0 {
		if p.direction == dirLong {
			drawdown := (p.extremePrice - tick.Bid1) / p.extremePrice
			if drawdown >= cfg.TrailingStop {
				p.closePos(cfg, tick, "跟踪止损", trades)
				return true, fmt.Sprintf("\033[33m多单跟踪止损\033[0m(抛压回撤%.2f%%)", drawdown*100)
			}
		} else {
			bounce := (tick.Ask1 - p.extremePrice) / p.extremePrice
			if bounce >= cfg.TrailingStop {
				p.closePos(cfg, tick, "跟踪止损", trades)
				return true, fmt.Sprintf("\033[33m空单跟踪止损\033[0m(反弹%.2f%%)", bounce*100)
			}
		}
	}

	if cfg.StopLoss > 0 && pct <= -cfg.StopLoss {
		p.closePos(cfg, tick, "保护止损", trades)
		return true, fmt.Sprintf("\033[31m硬止损(%.3f%%)\033[0m", pct*100)
	}

	// 动态止盈（时间衰减）：持仓越久，止盈标准越低，直至逼近保本线
	takeProfit := cfg.TakeProfit
	if cfg.DecaySec > 0 && takeProfit > 0 {
		holdSecs := time.Since(p.lastTradeTime).Seconds()
		if holdSecs > 0 {
			minTarget := cfg.TargetMin
			if minTarget == 0 {
				minTarget = 0.0015 // 默认保底 0.15%，恰好覆盖开平仓滑点和双边手续费
			}
			if takeProfit > minTarget {
				ratio := holdSecs / float64(cfg.DecaySec)
				if ratio > 1.0 {
					ratio = 1.0
				}
				exp := cfg.DecayExp
				if exp <= 0 {
					exp = 1.0
				}
				decayFactor := math.Pow(ratio, exp)
				takeProfit = takeProfit - (takeProfit-minTarget)*decayFactor
			}
		}
	}

	if takeProfit > 0 && pct >= takeProfit {
		p.closePos(cfg, tick, "固定止盈", trades)
		return true, fmt.Sprintf("\033[32m止盈(+%.3f%%)\033[0m", pct*100)
	}

	return false, ""
}
