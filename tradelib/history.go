package tradelib

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// PriceRecord 单条历史价格记录（时间戳 + 价格），与 collector 写入格式一致
type PriceRecord struct {
	Ts    time.Time `json:"ts"`
	Price float64   `json:"price"`
}

// LoadAllPriceRecordsFromDir 从数据目录加载所有历史价格记录（按时间顺序）。
// 目录结构：{dataDir}/{YYYY-MM-DD}/{HH}.jsonl
func LoadAllPriceRecordsFromDir(dataDir string) ([]PriceRecord, error) {
	dateEntries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, err
	}

	var dates []string
	for _, e := range dateEntries {
		if e.IsDir() {
			dates = append(dates, e.Name())
		}
	}
	sort.Strings(dates)

	var records []PriceRecord
	for _, date := range dates {
		dateDir := filepath.Join(dataDir, date)
		hourEntries, err := os.ReadDir(dateDir)
		if err != nil {
			continue
		}
		var hours []string
		for _, e := range hourEntries {
			if !e.IsDir() && filepath.Ext(e.Name()) == ".jsonl" {
				hours = append(hours, e.Name())
			}
		}
		sort.Strings(hours)
		for _, h := range hours {
			path := filepath.Join(dateDir, h)
			records = append(records, readPriceFileWithTs(path)...)
		}
	}
	return records, nil
}

// readPriceFileWithTs reads a JSONL file and returns PriceRecord entries with timestamps.
func readPriceFileWithTs(path string) []PriceRecord {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var records []PriceRecord
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var rec PriceRecord
		if json.Unmarshal(scanner.Bytes(), &rec) == nil && rec.Price > 0 {
			records = append(records, rec)
		}
	}
	return records
}
