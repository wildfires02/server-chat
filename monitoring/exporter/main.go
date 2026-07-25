package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/version"
)

type monitoringService int

const (
	promService   monitoringService = 1
	influxService monitoringService = 2
)

const (
	// InfluxDB 推送之间的最小间隔时间（单位：秒）。
	minPushInterval = 10
)

type promHTTPLogger struct{}

func (l promHTTPLogger) Println(v ...interface{}) {
	log.Println(v...)
}

func parseMetricList(list string) []string {
	metrics := strings.Split(list, ",")
	for i, m := range metrics {
		metrics[i] = strings.TrimSpace(m)
	}
	return metrics
}

// 编译器定义的构建版本号：
//
//	-ldflags "-X main.buildstamp=value_to_assign_to_buildstamp"
//
// 向客户端响应 {hi} 消息时汇报。
// 例如，要将 buildstamp 定义为服务端构建的时间戳，可以添加
// 编译命令行标志：
//
//	-ldflags "-X main.buildstamp=`date -u '+%Y%m%dT%H:%M:%SZ'`"
//
// 或者将其设置为 git 标签：
//
//	-ldflags "-X main.buildstamp=`git describe --tags`"
var buildstamp = "undef"

func main() {
	log.Printf("IM metrics exporter.")

	var (
		serveFor = flag.String("serve_for", "influxdb",
			"Monitoring service to gather metrics for. Available: influxdb, prometheus.")
		imAddr = flag.String("im_addr", "http://localhost:6060/stats/expvar",
			"Address of the IM instance to scrape.")
		listenAt = flag.String("listen_at", ":6222",
			"Host name and port to listen for incoming requests on.")
		metricList = flag.String("metric_list",
			"Version,LiveTopics,TotalTopics,LiveSessions,ClusterLeader,TotalClusterNodes,LiveClusterNodes,memstats.Alloc",
			"Comma-separated list of numeric metrics to scrape and export.")
		histoMetricList = flag.String("histo_metric_list",
			"RequestLatency,OutgoingMessageSize",
			"Comma-separated list of histogram metrics to scrape and export.")
		instance = flag.String("instance", "exporter",
			"Exporter instance name.")

		// Prometheus 特定参数。
		promNamespace   = flag.String("prom_namespace", "im", "Prometheus namespace for metrics '<namespace>_...'")
		promMetricsPath = flag.String("prom_metrics_path", "/metrics", "Path under which to expose metrics for Prometheus scrapes.")
		promTimeout     = flag.Int("prom_timeout", 15, "IM connection timeout in seconds in response to Prometheus scrapes.")

		// InfluxDB 特定参数。
		influxPushAddr = flag.String("influx_push_addr", "http://localhost:9999/write",
			"Address of InfluxDB target server where the data gets sent.")
		influxDBVersion = flag.String("influx_db_version", "1.7",
			"Version of InfluxDB (only 1.7 and 2.0 are supported).")
		influxOrganization = flag.String("influx_organization", "test",
			"InfluxDB organization to push metrics as.")
		influxBucket = flag.String("influx_bucket", "test",
			"InfluxDB storage bucket to store data in (used only in InfluxDB 2.0).")
		influxAuthToken = flag.String("influx_auth_token", "",
			"InfluxDB authentication token.")
		influxPushInterval = flag.Int("influx_push_interval", 30,
			"InfluxDB push interval in seconds.")
	)
	flag.Parse()

	var service monitoringService
	switch *serveFor {
	case "prometheus":
		service = promService
	case "influxdb":
		service = influxService
	default:
		log.Fatal("Invalid monitoring service:" + *serveFor + "; must be either \"prometheus\" or \"influxdb\"")
	}

	if service == influxService {
		if *influxPushInterval < minPushInterval {
			log.Printf("Push interval %d is too short. Resetting to %d", *influxPushInterval, minPushInterval)
			*influxPushInterval = minPushInterval
		}
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		var servingPath string
		switch service {
		case promService:
			servingPath = "<p>Prometheus exporter path: <a href='" + *promMetricsPath + "'>Metrics</a></p>"
		case influxService:
			servingPath = "<p>InfluxDB push path: <a href='/push'>Push</a></p>"
		}

		w.Write([]byte(`<html><head><title>IM Exporter</title></head><body>
<h1>IM Exporter</h1>
<p>Server type` + *serveFor + `</p>` + servingPath +
			`<h2>Build</h2>
<pre>` + version.Info() + ` ` + version.BuildContext() + `</pre>
</body></html>`))
	})

	metrics := parseMetricList(*metricList)
	histoMetrics := parseMetricList(*histoMetricList)
	scraper := Scraper{address: *imAddr, simpleMetrics: metrics, histogramMetrics: histoMetrics}
	var serverTypeString string
	// 创建导出器。
	switch service {
	case promService:
		serverTypeString = *serveFor
		promExporter := NewPromExporter(*imAddr, *promNamespace, time.Duration(*promTimeout)*time.Second, &scraper)
		registry := prometheus.NewRegistry()
		registry.MustRegister(promExporter)
		http.Handle(*promMetricsPath,
			promhttp.InstrumentMetricHandler(
				registry,
				promhttp.HandlerFor(
					registry,
					promhttp.HandlerOpts{
						ErrorLog: &promHTTPLogger{},
						Timeout:  time.Duration(*promTimeout) * time.Second,
					},
				),
			),
		)
	case influxService:
		serverTypeString = fmt.Sprintf("%s, version %s", *serveFor, *influxDBVersion)
		influxDBExporter := NewInfluxDBExporter(*influxDBVersion, *influxPushAddr, *influxOrganization, *influxBucket,
			*influxAuthToken, *instance, &scraper)
		if *influxPushInterval > 0 {
			go func() {
				interval := time.Duration(*influxPushInterval) * time.Second
				ticker := time.NewTicker(interval)
				defer ticker.Stop()
				for range ticker.C {
					if err := influxDBExporter.Push(); err != nil {
						log.Println("InfluxDB push failed:", err)
					}
				}
			}()
		} else {
			log.Println("InfluxDB push interval is zero. Will not push data automatically.")
		}
		// 强制进行数据推送。
		http.HandleFunc("/push", func(w http.ResponseWriter, r *http.Request) {
			var msg string
			if err := influxDBExporter.Push(); err == nil {
				msg = "HTTP 200 OK"
			} else {
				msg = err.Error()
			}

			w.Write([]byte(`<html><head><title>IM Push</title></head><body>
<h1>IM Push</h1>
<pre>` + msg + `</pre>
</body></html>`))
		})
	}

	log.Println("Reading IM expvar from", *imAddr)
	log.Printf("Exporter running at %s. Server type %s", *listenAt, serverTypeString)
	log.Fatalln(http.ListenAndServe(*listenAt, nil))
}
