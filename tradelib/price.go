package tradelib

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

// ===== HTTP 价格源（Binance）=====

var httpClient = &http.Client{Timeout: 5 * time.Second}

// fetchFromBinance_BBA 抓取币安盘口和最新价
func fetchFromBinance_BBA() (Tick, error) {
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

	if bid == 0 || ask == 0 {
		return Tick{}, fmt.Errorf("invalid binance book")
	}
	return Tick{
		Prc:  (bid + ask) / 2.0,
		Bid1: bid,
		Ask1: ask,
	}, nil
}

// ConvertHistoricalToTicks 将历史单价数组转换为伪造盘口 Tick（回测用）
func ConvertHistoricalToTicks(history []float64) []Tick {
	ticks := make([]Tick, len(history))
	for i, p := range history {
		ticks[i] = Tick{Prc: p, Bid1: p - 0.1, Ask1: p + 0.1}
	}
	return ticks
}

// FetchPrice 从价格源获取最新 BTC 行情
func FetchPrice() (Tick, error) {
	sources := []struct {
		name string
		fn   func() (Tick, error)
	}{
		{"BinanceBook", fetchFromBinance_BBA},
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
