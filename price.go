package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"
)

// ===== HTTP 价格源（OKX → Bybit → Binance）=====

var httpClient = &http.Client{Timeout: 5 * time.Second}

func fetchFromOKX() (float64, error) {
	resp, err := httpClient.Get("https://www.okx.com/api/v5/market/ticker?instId=BTC-USDT")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var result struct {
		Data []struct {
			Last string `json:"last"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}
	if len(result.Data) == 0 {
		return 0, fmt.Errorf("OKX 返回空数据")
	}
	return strconv.ParseFloat(result.Data[0].Last, 64)
}

func fetchFromBybit() (float64, error) {
	resp, err := httpClient.Get("https://api.bybit.com/v5/market/tickers?category=spot&symbol=BTCUSDT")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var result struct {
		Result struct {
			List []struct {
				LastPrice string `json:"lastPrice"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}
	if len(result.Result.List) == 0 {
		return 0, fmt.Errorf("Bybit 返回空数据")
	}
	return strconv.ParseFloat(result.Result.List[0].LastPrice, 64)
}

func fetchFromBinance() (float64, error) {
	resp, err := httpClient.Get("https://api.binance.com/api/v3/ticker/price?symbol=BTCUSDT")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var result struct {
		Price string `json:"price"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}
	return strconv.ParseFloat(result.Price, 64)
}

func fetchPrice() (float64, error) {
	sources := []struct {
		name string
		fn   func() (float64, error)
	}{
		{"OKX", fetchFromOKX},
		{"Bybit", fetchFromBybit},
		{"Binance", fetchFromBinance},
	}
	for _, src := range sources {
		price, err := src.fn()
		if err == nil {
			return price, nil
		}
		log.Printf("【%s】不可用: %v，尝试下一个源...", src.name, err)
	}
	return 0, fmt.Errorf("所有价格源均不可用")
}
