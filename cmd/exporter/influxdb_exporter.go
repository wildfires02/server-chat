// Package main 实现监控指标导出命令。
package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// InfluxDBExporter 从 IM 服务端收集指标并推送到 InfluxDB。
type InfluxDBExporter struct {
	// targetAddress 保存targetAddress。
	targetAddress string
	// organization 保存organization。
	organization string
	// bucket 保存bucket。
	bucket string
	// tokenHeader 保存令牌Header。
	tokenHeader string
	// instance 保存instance。
	instance string
	// scraper 保存scraper。
	scraper *Scraper
	// httpClient 保存HTTP客户端。
	httpClient *http.Client
}

// NewInfluxDBExporter 返回初始化的 InfluxDB 导出器。
func NewInfluxDBExporter(influxDBVersion, pushBaseAddress, organization,
	bucket, token, instance string, scraper *Scraper) *InfluxDBExporter {

	targetAddress := formPushTargetAddress(influxDBVersion, pushBaseAddress, organization, bucket)
	tokenHeader := formAuthorizationHeaderValue(influxDBVersion, token)
	return &InfluxDBExporter{
		targetAddress: targetAddress,
		organization:  organization,
		bucket:        bucket,
		tokenHeader:   tokenHeader,
		instance:      instance,
		scraper:       scraper,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// Push 从 IM 服务端抓取指标并将这些指标推送到 InfluxDB。
func (e *InfluxDBExporter) Push() error {
	metrics, err := e.scraper.CollectRaw()
	if err != nil {
		return err
	}
	b := new(bytes.Buffer)
	ts := time.Now().UnixNano()
	for k, v := range metrics {
		switch val := v.(type) {
		case float64:
			fmt.Fprintf(b, "%s,instance=%s value=%f %d\n", k, e.instance, val, ts)
		case *histogram:
			fmt.Fprintf(b, "%s,instance=%s count=%d %d\n", k, e.instance, val.count, ts)
			fmt.Fprintf(b, "%s,instance=%s sum=%f %d\n", k, e.instance, val.sum, ts)
			for bucket, count := range val.buckets {
				fmt.Fprintf(b, "%s,instance=%s le=%f,value=%d %d\n", k, e.instance, bucket, count, ts)
			}
		default:
			log.Printf("Warning: Invalid metric type for key %s: %T (%v)", k, v, v)
		}
	}
	req, err := http.NewRequest("POST", e.targetAddress, b)
	if err != nil {
		return err
	}
	req.Header.Add("Authorization", e.tokenHeader)
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var body string
		if rb, err := io.ReadAll(resp.Body); err != nil {
			body = err.Error()
		} else {
			body = strings.TrimSpace(string(rb))
		}

		return fmt.Errorf("HTTP %s: %s", resp.Status, body)
	}
	return nil
}

// formPushTargetAddress 完成formPushTargetAddress所需的内部处理。
func formPushTargetAddress(influxDBVersion, baseAddr, organization, bucket string) string {
	url, err := url.ParseRequestURI(baseAddr)
	if err != nil {
		log.Fatal("Invalid push_addr", err)
	}
	// URL 格式
	// - in 2.0: /api/v2/write?org=organization&bucket=bucket
	// - in 1.7: /write?db=organization
	organizationParamName := "org"
	bucketParamName := "bucket"
	if influxDBVersion == "1.7" {
		organizationParamName = "db"
		// 1.7 版本中缺少显式桶的概念。
		bucketParamName = ""
	}
	q := url.Query()
	q.Add(organizationParamName, organization)
	if bucketParamName != "" {
		q.Add(bucketParamName, bucket)
	}
	url.RawQuery = q.Encode()
	return url.String()
}

// formAuthorizationHeaderValue 完成formAuthorizationHeader值所需的内部处理。
func formAuthorizationHeaderValue(influxDBVersion, token string) string {
	// Authorization 请求头的值
	// - in 2.0: Token <token>
	// - in 1.7: Bearer <token>
	if influxDBVersion == "2.0" {
		return fmt.Sprintf("Token %s", token)
	}
	return fmt.Sprintf("Bearer %s", token)
}
