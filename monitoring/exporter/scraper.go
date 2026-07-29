// Package main 实现监控指标抓取和导出工具。
package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
)

// Scraper 从 IM 服务端收集指标。
type Scraper struct {
	// 目标 IM 服务端地址。
	address string
	// 要抓取的简单数值指标列表。
	simpleMetrics []string
	// 要抓取的直方图指标列表。
	histogramMetrics []string
	// 复用的带有超时设置的 HTTP 客户端。
	httpClient *http.Client
}

// 直方图结构体。
type histogram struct {
	// count 保存数量。
	count uint64
	// sum 保存sum。
	sum float64
	// buckets 按键索引buckets。
	buckets map[float64]uint64
}

// errKeyNotFound 保存err键NotFound的共享实例或运行状态。
var errKeyNotFound = errors.New("key not found")

// errMalformed 保存errMalformed的共享实例或运行状态。
var errMalformed = errors.New("input malformed")

// CollectRaw 从配置的 IM 实例收集所有指标，
// 并将其作为 map 返回。
func (s *Scraper) CollectRaw() (map[string]interface{}, error) {
	stats, err := s.Scrape()
	if err != nil {
		log.Println("Failed to fetch or parse response", err)
		return nil, err
	}
	metrics, err := s.parseStatsRaw(stats)
	if err != nil {
		return nil, err
	}
	metrics["up"] = 1.0
	return metrics, nil
}

// Scrape 使用 HTTP GET 从 IM 服务端获取数据并解码响应。
func (s *Scraper) Scrape() (map[string]interface{}, error) {
	if s.httpClient == nil {
		s.httpClient = &http.Client{
			Timeout: 10 * time.Second,
		}
	}
	resp, err := s.httpClient.Get(s.address)
	if err != nil {
		log.Println("Failed to connect to server", err)
		return nil, err
	}
	defer resp.Body.Close()

	var stats map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&stats)
	return stats, err
}

// parseStatsRaw 将输入解析为StatsRaw。
func (s *Scraper) parseStatsRaw(stats map[string]interface{}) (map[string]interface{}, error) {
	metrics := make(map[string]interface{})
	for _, key := range s.simpleMetrics {
		if val, err := parseNumeric(stats, key); err == nil {
			metrics[key] = val
		} else {
			log.Printf("Warning: metric %s not found in stats: %v", key, err)
		}
	}
	for _, key := range s.histogramMetrics {
		if val, err := parseHisto(stats, key); err == nil {
			metrics[key] = val
		} else {
			log.Printf("Warning: histogram metric %s not found in stats: %v", key, err)
		}
	}
	return metrics, nil
}

// 从 `stats` 中提取简单直方图并返回累积直方图
// 对应于简单直方图。
// 返回：(count, sum, buckets, 错误) 元组。
func parseHisto(stats map[string]interface{}, key string) (*histogram, error) {
	// 直方图以 JSON 形式呈现，具有预定义字段：count, sum, count_per_bucket, bounds。
	count, err := parseNumeric(stats, key+".count")
	if err != nil {
		return nil, err
	}
	sum, err := parseNumeric(stats, key+".sum")
	if err != nil {
		return nil, err
	}
	buckets, err := parseList(stats, key+".count_per_bucket")
	if err != nil {
		return nil, err
	}
	bounds, err := parseList(stats, key+".bounds")
	if err != nil {
		return nil, err
	}
	n := len(buckets)
	if n != len(bounds)+1 {
		return nil, errMalformed
	}
	result := make(map[float64]uint64)
	s := uint64(0)
	for i, v := range bounds {
		s += uint64(buckets[i])
		result[v] = s
	}
	return &histogram{count: uint64(count), sum: sum, buckets: result}, nil
}

// 针对给定路径从 `stats` 中提取数值列表。
func parseList(stats map[string]interface{}, path string) ([]float64, error) {
	value, err := parseMetric(stats, path)
	if err != nil {
		return nil, err
	}
	listval, ok := value.([]interface{})
	if !ok {
		log.Println("Value at path is not a float64 array:", path, value)
		return nil, errMalformed
	}
	result := make([]float64, 0, len(listval))
	for _, v := range listval {
		if f, ok := v.(float64); ok {
			result = append(result, f)
		}
	}
	return result, nil
}

// 针对给定路径从 `stats` 中提取数值。
func parseNumeric(stats map[string]interface{}, path string) (float64, error) {
	value, err := parseMetric(stats, path)
	if err != nil {
		return 0, err
	}
	floatval, ok := value.(float64)
	if !ok {
		log.Println("Value at path is not a float64:", path, value)
		return 0, errKeyNotFound
	}
	return floatval, nil
}

// 针对给定路径从 `stats` 中提取指标。
func parseMetric(stats map[string]interface{}, path string) (interface{}, error) {
	parts := strings.Split(path, ".")
	var value interface{}
	var found bool
	value = stats
	for i := 0; i < len(parts); i++ {
		subset, ok := value.(map[string]interface{})
		if !ok {
			log.Println("Invalid key path:", path)
			return 0, errKeyNotFound
		}
		value, found = subset[parts[i]]
		if !found {
			log.Println("Invalid key path:", path, "(", parts[i], ")")
			return 0, errKeyNotFound
		}
	}

	return value, nil
}
