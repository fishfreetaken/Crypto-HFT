package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"
)

// ===== 数据结构：带盘口的高频快照 =====

type Tick struct {
	Prc  float64 // 最新成交价
	Bid1 float64 // 买一价 (做空成交价/平多价)
	Ask1 float64 // 卖一价 (做多成交价/平空价)
	Vol  float64 // 成交量 (可选保留)
}

func (t Tick) String() string {
	return fmt.Sprintf("P_%.2f_B_%.2f_A_%.2f", t.Prc, t.Bid1, t.Ask1)
}

// ===== HTTP 价格源（OKX → Bybit → Binance）=====

var httpClient = &http.Client{Timeout: 5 * time.Second}

// fetchFromBinance_BBA 抓取币安盘口和最新价 (Bid/Ask/Prc)
func fetchFromBinance_BBA() (Tick, error) {
	// 并发两笔请求，或用更简便的 Ticker 接口 (ticker/bookTicker 含 bid/ask, ticker/price 含最新价)
	// 由于这只是外壳演示，直接拿书本订单 (bookTicker)
	resp, err := httpClient.Get("https://api.binance.com/api/v3/ticker/bookTicker?symbol=BTCUSDT")
	if err != nil {
		return Tick{}, err
	}
	defer resp.Body.Close()

	var book struct {
		BidPrice string `json:"bidPrice"`
		AskPrice string `json:"askPrice"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&book); err != nil {
		return Tick{}, err
	}
	
	bid, _ := strconv.ParseFloat(book.BidPrice, 64)
	ask, _ := strconv.ParseFloat(book.AskPrice, 64)
	
	// 近似取中间价作为Prc，如果有单独的 price 接口更精确，但 HFT 这里 bid/ask 足以。
	if bid == 0 || ask == 0 {
		return Tick{}, fmt.Errorf("invalid binance book")
	}
	return Tick{
		Prc:  (bid + ask) / 2.0,
		Bid1: bid,
		Ask1: ask,
	}, nil
}

// 兼容老版本的历史单价加载函数（回测部分，如果是 CSV 可以改这里）
// 目前为了让旧的 float64 历史数据转配为伪造的 Tick：
func convertHistoricalToTicks(history []float64) []Tick {
	ticks := make([]Tick, len(history))
	for i, p := range history {
		// 历史数据如果是一维价格，强制加 0.1 的散点模拟 bid/ask 点差
		ticks[i] = Tick{Prc: p, Bid1: p - 0.1, Ask1: p + 0.1}
	}
	return ticks
}

func fetchPrice() (Tick, error) {
	sources := []struct {
		name string
		fn   func() (Tick, error)
	}{
		{"BinanceBook", fetchFromBinance_BBA},
		// Bybit / OKX 可以同样改写成 BookTicker，这里先强行主用 Binance
	}
	for _, src := range sources {
		tick, err := src.fn()
		if err == nil {
			return tick, nil
		}
		log.Printf("【%s】不可用: %v，尝试下一个源...", src.name, err)
	}
	return Tick{}, fmt.Errorf("所有价格源均不可用")
}
