package main

import (
	"fmt"
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
	lastTradeTime time.Time
	longCount     int
	shortCount    int
	closeCount    int
	peakEquity    float64
}

func (p *portfolio) inPosition() bool {
	return p.direction != dirNone
}

func (p *portfolio) totalEquity(cfg Config, price float64) float64 {
	if !p.inPosition() {
		return p.cash
	}
	pricePct := (price-p.entryPrice)/p.entryPrice * float64(p.direction)
	return p.margin * (1 + cfg.Leverage*pricePct)
}

func (p *portfolio) positionPct(price float64) float64 {
	if p.entryPrice == 0 {
		return 0
	}
	return (price-p.entryPrice)/p.entryPrice * float64(p.direction)
}

func (p *portfolio) openPos(cfg Config, d posDir, price float64, trades *[]tradeRecord) {
	openFee := p.cash * cfg.Leverage * cfg.TradeFee
	p.margin = p.cash - openFee
	p.cash = 0
	p.direction = d
	p.entryPrice = price
	p.lastTradeTime = time.Now()

	action := "做多"
	if d == dirLong {
		p.longCount++
	} else {
		action = "做空"
		p.shortCount++
	}
	*trades = append(*trades, tradeRecord{
		ts: time.Now(), action: action, price: price, equity: p.margin,
	})
	fmt.Printf("[%s] [%-4s] \033[36m%s\033[0m @ $%.2f | 保证金:$%.2f 杠杆:%.0fx\n",
		time.Now().Format("15:04:05"), p.name, action, price, p.margin, cfg.Leverage)
}

func (p *portfolio) closePos(cfg Config, price float64, reason string, trades *[]tradeRecord) {
	pricePct := p.positionPct(price)
	closeFee := p.margin * cfg.Leverage * cfg.TradeFee
	newCash := p.margin*(1+cfg.Leverage*pricePct) - closeFee
	if newCash < 0 {
		newCash = 0
	}
	equityChangePct := (newCash/p.margin - 1) * 100

	fmt.Printf("[%s] [%-4s] \033[33m平仓(%s)\033[0m @ $%.2f | %s仓 | 价格:%+.3f%% | $%.2f→$%.2f(%+.2f%%)\n",
		time.Now().Format("15:04:05"), p.name, reason, price, p.direction,
		pricePct*100, p.margin, newCash, equityChangePct)

	*trades = append(*trades, tradeRecord{
		ts: time.Now(), action: "平仓(" + reason + ")", price: price, equity: newCash,
	})
	p.closeCount++
	p.lastTradeTime = time.Now()
	p.cash = newCash
	p.margin = 0
	p.direction = dirNone
	p.entryPrice = 0
}
