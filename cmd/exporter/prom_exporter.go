// Package main 实现监控指标导出命令。
package main

import (
	"log"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// PromExporter 从 IM 服务端收集 Prometheus 格式的指标。
type PromExporter struct {
	// address 保存address。
	address string
	// timeout 保存超时时间。
	timeout time.Duration
	// namespace 保存namespace。
	namespace string

	// scraper 保存scraper。
	scraper *Scraper

	// up 保存up。
	up *prometheus.Desc
	// version 保存版本。
	version *prometheus.Desc
	// topicsLive 保存topicsLive。
	topicsLive *prometheus.Desc
	// topicsTotal 保存topicsTotal。
	topicsTotal *prometheus.Desc
	// sessionsLive 保存sessionsLive。
	sessionsLive *prometheus.Desc
	// sessionsTotal 保存sessionsTotal。
	sessionsTotal *prometheus.Desc

	// numGoroutines 保存numGoroutines。
	numGoroutines *prometheus.Desc

	// incomingMessagesWebsockTotal 保存incomingMessagesWebsockTotal。
	incomingMessagesWebsockTotal *prometheus.Desc
	// outgoingMessagesWebsockTotal 保存outgoingMessagesWebsockTotal。
	outgoingMessagesWebsockTotal *prometheus.Desc

	// incomingMessagesLongpollTotal 保存incomingMessagesLongpollTotal。
	incomingMessagesLongpollTotal *prometheus.Desc
	// outgoingMessagesLongpollTotal 保存outgoingMessagesLongpollTotal。
	outgoingMessagesLongpollTotal *prometheus.Desc

	// incomingMessagesGrpcTotal 保存incomingMessagesgRPCTotal。
	incomingMessagesGrpcTotal *prometheus.Desc
	// outgoingMessagesGrpcTotal 保存outgoingMessagesgRPCTotal。
	outgoingMessagesGrpcTotal *prometheus.Desc

	// fileDownloadsTotal 保存文件DownloadsTotal。
	fileDownloadsTotal *prometheus.Desc
	// fileUploadsTotal 保存文件UploadsTotal。
	fileUploadsTotal *prometheus.Desc

	// ctrlCodesTotal2xx 保存ctrlCodesTotal2xx。
	ctrlCodesTotal2xx *prometheus.Desc
	// ctrlCodesTotal3xx 保存ctrlCodesTotal3xx。
	ctrlCodesTotal3xx *prometheus.Desc
	// ctrlCodesTotal4xx 保存ctrlCodesTotal4xx。
	ctrlCodesTotal4xx *prometheus.Desc
	// ctrlCodesTotal5xx 保存ctrlCodesTotal5xx。
	ctrlCodesTotal5xx *prometheus.Desc

	// clusterLeader 保存集群Leader。
	clusterLeader *prometheus.Desc
	// clusterSize 保存集群Size。
	clusterSize *prometheus.Desc
	// clusterNodesLive 保存集群NodesLive。
	clusterNodesLive *prometheus.Desc
	// malloced 保存malloced。
	malloced *prometheus.Desc
	// requestLatencyMsCount 保存请求LatencyMs数量。
	requestLatencyMsCount *prometheus.Desc
	// outgoingMessageBytesCount 保存outgoing消息Bytes数量。
	outgoingMessageBytesCount *prometheus.Desc
}

// NewPromExporter 返回初始化的 Prometheus 导出器。
func NewPromExporter(server, namespace string, timeout time.Duration, scraper *Scraper) *PromExporter {
	return &PromExporter{
		address:   server,
		timeout:   timeout,
		namespace: namespace,
		scraper:   scraper,
		up: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "up"),
			"If IM instance is reachable.",
			nil,
			nil,
		),
		version: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "version"),
			"The version of this IM instance.",
			nil,
			nil,
		),
		topicsLive: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "topics_live_count"),
			"Number of currently active topics.",
			nil,
			nil,
		),
		topicsTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "topics_total"),
			"Total number of topics used during instance lifetime.",
			nil,
			nil,
		),
		sessionsLive: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "sessions_live_count"),
			"Number of currently active sessions.",
			nil,
			nil,
		),
		sessionsTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "sessions_total"),
			"Total number of sessions since instance start.",
			nil,
			nil,
		),
		numGoroutines: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "num_goroutines"),
			"Number of currently spawned goroutines.",
			nil,
			nil,
		),
		incomingMessagesWebsockTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "incoming_messages_websock_total"),
			"Total number of incoming messages via websocket.",
			nil,
			nil,
		),
		outgoingMessagesWebsockTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "outgoing_messages_websock_total"),
			"Total number of outgoiing messages via websocket.",
			nil,
			nil,
		),
		incomingMessagesLongpollTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "incoming_messages_longpoll_total"),
			"Total number of incoming messages via longpoll.",
			nil,
			nil,
		),
		outgoingMessagesLongpollTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "outgoing_messages_longpoll_total"),
			"Total number of outgoiing messages via longpoll.",
			nil,
			nil,
		),
		incomingMessagesGrpcTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "incoming_messages_grpc_total"),
			"Total number of incoming messages via grpc.",
			nil,
			nil,
		),
		outgoingMessagesGrpcTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "outgoing_messages_grpc_total"),
			"Total number of outgoiing messages via grpc.",
			nil,
			nil,
		),
		fileDownloadsTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "file_downloads_total"),
			"Total number of large file downloads.",
			nil,
			nil,
		),
		fileUploadsTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "file_uploads_total"),
			"Total number of large file uploads.",
			nil,
			nil,
		),
		ctrlCodesTotal2xx: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "ctrl_codes_total_2xx"),
			"Total number of 2xx ctrl response codes.",
			nil,
			nil,
		),
		ctrlCodesTotal3xx: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "ctrl_codes_total_3xx"),
			"Total number of 3xx ctrl response codes.",
			nil,
			nil,
		),
		ctrlCodesTotal4xx: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "ctrl_codes_total_4xx"),
			"Total number of 4xx ctrl response codes.",
			nil,
			nil,
		),
		ctrlCodesTotal5xx: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "ctrl_codes_total_5xx"),
			"Total number of 5xx ctrl response codes.",
			nil,
			nil,
		),
		clusterLeader: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "cluster_leader"),
			"If this cluster node is the cluster leader.",
			nil,
			nil,
		),
		clusterSize: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "cluster_size"),
			"Configured number of cluster nodes.",
			nil,
			nil,
		),
		clusterNodesLive: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "cluster_nodes_live"),
			"Number of cluster nodes believed to be live by the current node.",
			nil,
			nil,
		),
		malloced: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "malloced_bytes"),
			"Number of bytes of memory allocated and in use.",
			nil,
			nil,
		),
		requestLatencyMsCount: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "request_latency_ms_count"),
			"Request latency histogram (in ms).",
			nil,
			nil,
		),
		outgoingMessageBytesCount: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "outgoing_message_bytes"),
			"Response size histogram (in bytes).",
			nil,
			nil,
		),
	}
}

// Describe 描述由导出器导出所有指标。它
// 实现了 prometheus.Collector 接口。
func (e *PromExporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- e.up
	ch <- e.version
	ch <- e.topicsLive
	ch <- e.topicsTotal
	ch <- e.sessionsLive
	ch <- e.sessionsTotal
	ch <- e.numGoroutines

	ch <- e.incomingMessagesWebsockTotal
	ch <- e.outgoingMessagesWebsockTotal

	ch <- e.incomingMessagesLongpollTotal
	ch <- e.outgoingMessagesLongpollTotal

	ch <- e.incomingMessagesGrpcTotal
	ch <- e.outgoingMessagesGrpcTotal

	ch <- e.fileDownloadsTotal
	ch <- e.fileUploadsTotal

	ch <- e.ctrlCodesTotal2xx
	ch <- e.ctrlCodesTotal3xx
	ch <- e.ctrlCodesTotal4xx
	ch <- e.ctrlCodesTotal5xx

	ch <- e.clusterLeader
	ch <- e.clusterSize
	ch <- e.clusterNodesLive
	ch <- e.malloced

	ch <- e.requestLatencyMsCount
	ch <- e.outgoingMessageBytesCount
}

// Collect 从配置的 IM 实例获取统计信息，以及
// delivers them as Prometheus metrics. It 实现了 prometheus.Collector 接口。
func (e *PromExporter) Collect(ch chan<- prometheus.Metric) {
	up := float64(1)
	if stats, err := e.scraper.Scrape(); err != nil {
		log.Println("Failed to fetch or parse response", err)
		up = 0
	} else {
		if err := e.parseStats(ch, stats); err != nil {
			up = 0
		}
	}

	ch <- prometheus.MustNewConstMetric(e.up, prometheus.GaugeValue, up)
}

// parseStats 将输入解析为Stats。
func (e *PromExporter) parseStats(ch chan<- prometheus.Metric, stats map[string]interface{}) error {
	err := firstError(
		e.parseAndUpdate(ch, e.version, prometheus.GaugeValue, stats, "Version"),
		e.parseAndUpdate(ch, e.topicsLive, prometheus.GaugeValue, stats, "LiveTopics"),
		e.parseAndUpdate(ch, e.topicsTotal, prometheus.CounterValue, stats, "TotalTopics"),
		e.parseAndUpdate(ch, e.sessionsLive, prometheus.GaugeValue, stats, "LiveSessions"),
		e.parseAndUpdate(ch, e.sessionsTotal, prometheus.CounterValue, stats, "TotalSessions"),
		e.parseAndUpdate(ch, e.numGoroutines, prometheus.GaugeValue, stats, "NumGoroutines"),

		e.parseAndUpdate(ch, e.incomingMessagesWebsockTotal, prometheus.CounterValue, stats, "IncomingMessagesWebsockTotal"),
		e.parseAndUpdate(ch, e.outgoingMessagesWebsockTotal, prometheus.CounterValue, stats, "OutgoingMessagesWebsockTotal"),

		e.parseAndUpdate(ch, e.incomingMessagesLongpollTotal, prometheus.CounterValue, stats, "IncomingMessagesLongpollTotal"),
		e.parseAndUpdate(ch, e.outgoingMessagesLongpollTotal, prometheus.CounterValue, stats, "OutgoingMessagesLongpollTotal"),

		e.parseAndUpdate(ch, e.incomingMessagesGrpcTotal, prometheus.CounterValue, stats, "IncomingMessagesGrpcTotal"),
		e.parseAndUpdate(ch, e.outgoingMessagesGrpcTotal, prometheus.CounterValue, stats, "OutgoingMessagesGrpcTotal"),

		e.parseAndUpdate(ch, e.fileDownloadsTotal, prometheus.CounterValue, stats, "FileDownloadsTotal"),
		e.parseAndUpdate(ch, e.fileUploadsTotal, prometheus.CounterValue, stats, "FileUploadsTotal"),

		e.parseAndUpdate(ch, e.ctrlCodesTotal2xx, prometheus.CounterValue, stats, "CtrlCodesTotal2xx"),
		e.parseAndUpdate(ch, e.ctrlCodesTotal3xx, prometheus.CounterValue, stats, "CtrlCodesTotal3xx"),
		e.parseAndUpdate(ch, e.ctrlCodesTotal4xx, prometheus.CounterValue, stats, "CtrlCodesTotal4xx"),
		e.parseAndUpdate(ch, e.ctrlCodesTotal5xx, prometheus.CounterValue, stats, "CtrlCodesTotal5xx"),

		e.parseAndUpdate(ch, e.clusterLeader, prometheus.GaugeValue, stats, "ClusterLeader"),
		e.parseAndUpdate(ch, e.clusterSize, prometheus.GaugeValue, stats, "TotalClusterNodes"),
		e.parseAndUpdate(ch, e.clusterNodesLive, prometheus.GaugeValue, stats, "LiveClusterNodes"),
		e.parseAndUpdate(ch, e.malloced, prometheus.GaugeValue, stats, "memstats.Alloc"),

		e.parseAndUpdateHisto(ch, e.requestLatencyMsCount, stats, "RequestLatency"),
		e.parseAndUpdateHisto(ch, e.outgoingMessageBytesCount, stats, "OutgoingMessageSize"),
	)

	return err
}

// parseAndUpdate 将输入解析为AndUpdate。
func (e *PromExporter) parseAndUpdate(ch chan<- prometheus.Metric, desc *prometheus.Desc, valueType prometheus.ValueType,
	stats map[string]interface{}, key string) error {
	v, err := parseNumeric(stats, key)
	if err != nil {
		return err
	}
	ch <- prometheus.MustNewConstMetric(desc, valueType, v)
	return nil
}

// parseAndUpdateHisto 将输入解析为AndUpdateHisto。
func (e *PromExporter) parseAndUpdateHisto(ch chan<- prometheus.Metric, desc *prometheus.Desc,
	stats map[string]interface{}, key string) error {
	h, err := parseHisto(stats, key)
	if err != nil {
		return err
	}
	ch <- prometheus.MustNewConstHistogram(desc, h.count, h.sum, h.buckets)
	return nil
}

// firstError 完成first错误所需的内部处理。
func firstError(errs ...error) error {
	for _, v := range errs {
		if v != nil {
			return v
		}
	}
	return nil
}
