package tradelib

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ===== 数据结构：带盘口的高频快照 =====

type Tick struct {
	Prc    float64 // 最新成交价
	Bid1   float64 // 买一价 (做空成交价/平多价)
	Ask1   float64 // 卖一价 (做多成交价/平空价)
	BidVol float64 // 买盘挂单量
	AskVol float64 // 卖盘挂单量
}

func (t Tick) String() string {
	return fmt.Sprintf("P_%.2f_B_%.2f_A_%.2f", t.Prc, t.Bid1, t.Ask1)
}

// ===== HTTP 价格源（Binance）=====

var httpClient = &http.Client{Timeout: 5 * time.Second}

// fetchFromOKX 抓取 OKX 盘口和最新价
func fetchFromOKX() (Tick, error) {
	resp, err := httpClient.Get("https://www.okx.com/api/v5/market/ticker?instId=BTC-USDT")
	if err != nil {
		return Tick{}, err
	}
	defer resp.Body.Close()
	var result struct {
		Data []struct {
			Last  string `json:"last"`
			Bid   string `json:"bidPx"`
			Ask   string `json:"askPx"`
			BidSz string `json:"bidSz"`
			AskSz string `json:"askSz"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return Tick{}, err
	}
	if len(result.Data) == 0 {
		return Tick{}, fmt.Errorf("OKX 返回空数据")
	}
	d := result.Data[0]
	last, _ := strconv.ParseFloat(d.Last, 64)
	bid, _ := strconv.ParseFloat(d.Bid, 64)
	ask, _ := strconv.ParseFloat(d.Ask, 64)
	bv, _ := strconv.ParseFloat(d.BidSz, 64)
	av, _ := strconv.ParseFloat(d.AskSz, 64)

	if bid == 0 || ask == 0 {
		bid, ask = last-0.1, last+0.1
	}
	return Tick{Prc: last, Bid1: bid, Ask1: ask, BidVol: bv, AskVol: av}, nil
}

// fetchFromBybit 抓取 Bybit 盘口和最新价
func fetchFromBybit() (Tick, error) {
	resp, err := httpClient.Get("https://api.bybit.com/v5/market/tickers?category=spot&symbol=BTCUSDT")
	if err != nil {
		return Tick{}, err
	}
	defer resp.Body.Close()
	var result struct {
		Result struct {
			List []struct {
				LastPrice string `json:"lastPrice"`
				Bid1Price string `json:"bid1Price"`
				Ask1Price string `json:"ask1Price"`
				Bid1Size  string `json:"bid1Size"`
				Ask1Size  string `json:"ask1Size"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return Tick{}, err
	}
	if len(result.Result.List) == 0 {
		return Tick{}, fmt.Errorf("Bybit 返回空数据")
	}
	d := result.Result.List[0]
	last, _ := strconv.ParseFloat(d.LastPrice, 64)
	bid, _ := strconv.ParseFloat(d.Bid1Price, 64)
	ask, _ := strconv.ParseFloat(d.Ask1Price, 64)
	bv, _ := strconv.ParseFloat(d.Bid1Size, 64)
	av, _ := strconv.ParseFloat(d.Ask1Size, 64)

	if bid == 0 || ask == 0 {
		bid, ask = last-0.1, last+0.1
	}
	return Tick{Prc: last, Bid1: bid, Ask1: ask, BidVol: bv, AskVol: av}, nil
}

// fetchFromBinance_BBA 抓取币安盘口和最新价
func fetchFromBinance_BBA() (Tick, error) {
	resp, err := httpClient.Get("https://api.binance.com/api/v3/ticker/bookTicker?symbol=BTCUSDT")
	if err != nil {
		return Tick{}, err
	}
	defer resp.Body.Close()

	var result struct {
		Price    string `json:"price"` // Not available in bookTicker
		BidPrice string `json:"bidPrice"`
		AskPrice string `json:"askPrice"`
		BidQty   string `json:"bidQty"`
		AskQty   string `json:"askQty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return Tick{}, err
	}

	bid, _ := strconv.ParseFloat(result.BidPrice, 64)
	ask, _ := strconv.ParseFloat(result.AskPrice, 64)
	last := (bid + ask) / 2 // Approximate last price since we used bookTicker
	bv, _ := strconv.ParseFloat(result.BidQty, 64)
	av, _ := strconv.ParseFloat(result.AskQty, 64)

	if bid == 0 || ask == 0 {
		return Tick{}, fmt.Errorf("Binance 返回空盘口")
	}
	return Tick{Prc: last, Bid1: bid, Ask1: ask, BidVol: bv, AskVol: av}, nil
}

// ConvertHistoricalToTicks 将历史单价数组转换为伪造盘口 Tick（回测用）
func ConvertHistoricalToTicks(history []float64) []Tick {
	ticks := make([]Tick, len(history))
	for i, p := range history {
		ticks[i] = Tick{Prc: p, Bid1: p - 0.1, Ask1: p + 0.1}
	}
	return ticks
}

// fetchFromCollectorLocal 通过读取采集器最新写入的文件获取价格
func fetchFromCollectorLocal(dataDir string) (Tick, error) {
	now := time.Now()
	path := filepath.Join(dataDir, now.Format("2006-01-02"), now.Format("15")+".jsonl")

	f, err := os.Open(path)
	if err != nil {
		return Tick{}, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return Tick{}, err
	}
	size := stat.Size()
	if size == 0 {
		return Tick{}, fmt.Errorf("empty file")
	}

	readSize := int64(256)
	if size < readSize {
		readSize = size
	}
	buf := make([]byte, readSize)
	_, err = f.ReadAt(buf, size-readSize)
	if err != nil && err.Error() != "EOF" {
		return Tick{}, err
	}

	lines := strings.Split(strings.TrimSpace(string(buf)), "\n")
	if len(lines) == 0 {
		return Tick{}, fmt.Errorf("no lines")
	}
	lastLine := lines[len(lines)-1]

	var rec struct {
		Price  float64 `json:"price"`
		Bid    float64 `json:"bid,omitempty"`
		Ask    float64 `json:"ask,omitempty"`
		BidVol float64 `json:"bid_vol,omitempty"`
		AskVol float64 `json:"ask_vol,omitempty"`
	}
	if err := json.Unmarshal([]byte(lastLine), &rec); err != nil {
		return Tick{}, err
	}

	bid, ask := rec.Bid, rec.Ask
	if bid == 0 || ask == 0 {
		bid, ask = rec.Price-0.1, rec.Price+0.1
	}

	return Tick{
		Prc:    rec.Price,
		Bid1:   bid,
		Ask1:   ask,
		BidVol: rec.BidVol,
		AskVol: rec.AskVol,
	}, nil
}

// FetchPrice 从价格源获取最新 BTC 行情 (顺序: 本地 Collector -> OKX -> Bybit -> Binance)
func FetchPrice(dataDir string) (Tick, error) {
	sources := []struct {
		name string
		fn   func() (Tick, error)
	}{
		{"CollectorLocal", func() (Tick, error) { return fetchFromCollectorLocal(dataDir) }},
		{"OKX", fetchFromOKX},
		{"Bybit", fetchFromBybit},
		{"BinanceBook", fetchFromBinance_BBA},
	}
	for _, src := range sources {
		tick, err := src.fn()
		if err == nil {
			return tick, nil
		}
		// 降低本地文件未找到的日志刷屏级别
		if src.name != "CollectorLocal" || !os.IsNotExist(err) {
			log.Printf("【%s】不可用: %v，尝试下一个源...", src.name, err)
		}
	}
	return Tick{}, fmt.Errorf("所有价格源均不可用")
}
