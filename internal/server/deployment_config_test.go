package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestValidateDeploymentConfig 验证部署模式门禁覆盖单机、集群和生产安全规则。
func TestValidateDeploymentConfig(t *testing.T) {
	tests := []struct {
		name                string
		runtime             runtimeConfig
		cluster             clusterConfig
		store               json.RawMessage
		clusterSelfOverride string
		pprofURL            string
		wantError           string
	}{
		{
			name: "开发单机",
			runtime: runtimeConfig{
				Environment:    environmentDevelopment,
				DeploymentMode: deploymentModeStandalone,
			},
		},
		{
			name: "测试单机",
			runtime: runtimeConfig{
				Environment:    environmentTest,
				DeploymentMode: deploymentModeStandalone,
			},
		},
		{
			name: "生产禁止单机",
			runtime: runtimeConfig{
				Environment:    environmentProduction,
				DeploymentMode: deploymentModeStandalone,
			},
			wantError: "production 环境必须使用 cluster",
		},
		{
			name: "预发布禁止单机",
			runtime: runtimeConfig{
				Environment:    environmentStaging,
				DeploymentMode: deploymentModeStandalone,
			},
			wantError: "staging 环境必须使用 cluster",
		},
		{
			name: "单机模式拒绝集群节点覆盖",
			runtime: runtimeConfig{
				Environment:    environmentDevelopment,
				DeploymentMode: deploymentModeStandalone,
			},
			cluster:             validClusterConfig(),
			clusterSelfOverride: "im-0",
			wantError:           "standalone 模式不能配置集群节点身份",
		},
		{
			name: "开发集群允许命令行节点身份",
			runtime: runtimeConfig{
				Environment:    environmentDevelopment,
				DeploymentMode: deploymentModeCluster,
			},
			cluster:             validClusterConfig(),
			clusterSelfOverride: "im-1",
		},
		{
			name: "集群节点身份不能为空",
			runtime: runtimeConfig{
				Environment:    environmentDevelopment,
				DeploymentMode: deploymentModeCluster,
			},
			cluster:   clusterConfig{Nodes: validClusterConfig().Nodes},
			wantError: "必须配置 cluster_config.self",
		},
		{
			name: "当前节点必须属于成员列表",
			runtime: runtimeConfig{
				Environment:    environmentDevelopment,
				DeploymentMode: deploymentModeCluster,
			},
			cluster: clusterConfig{
				ThisName: "im-9",
				Nodes:    validClusterConfig().Nodes,
			},
			wantError: "不在 cluster_config.nodes",
		},
		{
			name: "节点名称不能重复",
			runtime: runtimeConfig{
				Environment:    environmentDevelopment,
				DeploymentMode: deploymentModeCluster,
			},
			cluster: clusterConfig{
				ThisName: "im-0",
				Nodes: []clusterNodeConfig{
					{Name: "im-0", Addr: "im-0:12000"},
					{Name: "im-0", Addr: "im-1:12000"},
				},
			},
			wantError: "节点名称",
		},
		{
			name: "生产集群配置有效",
			runtime: runtimeConfig{
				Environment:    environmentProduction,
				DeploymentMode: deploymentModeCluster,
			},
			cluster: validClusterConfig(),
		},
		{
			name: "生产集群副本数必须为奇数",
			runtime: runtimeConfig{
				Environment:    environmentProduction,
				DeploymentMode: deploymentModeCluster,
			},
			cluster: func() clusterConfig {
				config := validClusterConfig()
				config.ExpectedReplicas = 2
				return config
			}(),
			wantError: "expected_replicas",
		},
		{
			name: "生产集群允许预声明五个候选节点并以三节点启动",
			runtime: runtimeConfig{
				Environment:    environmentProduction,
				DeploymentMode: deploymentModeCluster,
			},
			cluster: func() clusterConfig {
				config := validClusterConfig()
				config.InitialMembers = []string{"im-0", "im-1", "im-2"}
				config.Nodes = append(
					config.Nodes,
					clusterNodeConfig{Name: "im-3", Addr: "im-3:12000"},
					clusterNodeConfig{Name: "im-4", Addr: "im-4:12000"},
				)
				return config
			}(),
		},
		{
			name: "候选节点多于初始副本时必须声明初始成员",
			runtime: runtimeConfig{
				Environment:    environmentProduction,
				DeploymentMode: deploymentModeCluster,
			},
			cluster: func() clusterConfig {
				config := validClusterConfig()
				config.Nodes = append(
					config.Nodes,
					clusterNodeConfig{Name: "im-3", Addr: "im-3:12000"},
					clusterNodeConfig{Name: "im-4", Addr: "im-4:12000"},
				)
				return config
			}(),
			wantError: "initial_members",
		},
		{
			name: "生产集群必须配置控制面",
			runtime: runtimeConfig{
				Environment:    environmentProduction,
				DeploymentMode: deploymentModeCluster,
			},
			cluster: func() clusterConfig {
				config := validClusterConfig()
				config.ControlPlane = nil
				return config
			}(),
			wantError: "必须配置 cluster_config.control_plane",
		},
		{
			name: "生产集群 etcd 必须配置 mTLS",
			runtime: runtimeConfig{
				Environment:    environmentProduction,
				DeploymentMode: deploymentModeCluster,
			},
			cluster: func() clusterConfig {
				config := validClusterConfig()
				config.ControlPlane.TLS = nil
				return config
			}(),
			wantError: "必须配置 cluster_config.control_plane.tls",
		},
		{
			name: "生产集群必须配置 gRPC Lane",
			runtime: runtimeConfig{
				Environment:    environmentProduction,
				DeploymentMode: deploymentModeCluster,
			},
			cluster: func() clusterConfig {
				config := validClusterConfig()
				config.Transport = nil
				return config
			}(),
			wantError: "必须配置 cluster_config.transport",
		},
		{
			name: "生产集群必须配置 mTLS",
			runtime: runtimeConfig{
				Environment:    environmentProduction,
				DeploymentMode: deploymentModeCluster,
			},
			cluster: func() clusterConfig {
				config := validClusterConfig()
				config.TLS = nil
				return config
			}(),
			wantError: "必须配置 cluster_config.tls",
		},
		{
			name: "生产环境禁止 pprof",
			runtime: runtimeConfig{
				Environment:    environmentProduction,
				DeploymentMode: deploymentModeCluster,
			},
			cluster:   validClusterConfig(),
			pprofURL:  "/pprof",
			wantError: "禁止通过 pprof_url",
		},
		{
			name: "控制面拒绝 RethinkDB",
			runtime: runtimeConfig{
				Environment:    environmentProduction,
				DeploymentMode: deploymentModeCluster,
			},
			cluster:   validClusterConfig(),
			store:     json.RawMessage(`{"use_adapter":"rethinkdb","adapters":{"rethinkdb":{}}}`),
			wantError: "RethinkDB 不支持",
		},
		{
			name: "MongoDB 集群必须启用 Replica Set 事务",
			runtime: runtimeConfig{
				Environment:    environmentProduction,
				DeploymentMode: deploymentModeCluster,
			},
			cluster:   validClusterConfig(),
			store:     json.RawMessage(`{"use_adapter":"mongodb","adapters":{"mongodb":{}}}`),
			wantError: "必须配置 replica_set",
		},
		{
			name: "MongoDB Replica Set 可通过集群存储门禁",
			runtime: runtimeConfig{
				Environment:    environmentProduction,
				DeploymentMode: deploymentModeCluster,
			},
			cluster: validClusterConfig(),
			store: json.RawMessage(
				`{"use_adapter":"mongodb","adapters":{"mongodb":{"replica_set":"rs0"}}}`,
			),
		},
		{
			name:      "运行环境必须显式配置",
			wantError: "runtime.environment",
		},
		{
			name: "部署模式必须显式配置",
			runtime: runtimeConfig{
				Environment: environmentDevelopment,
			},
			wantError: "runtime.deployment_mode",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rawCluster, err := json.Marshal(test.cluster)
			if err != nil {
				t.Fatal(err)
			}
			config := configType{
				Runtime: test.runtime,
				Cluster: rawCluster,
				Store:   test.store,
			}
			if len(config.Store) == 0 {
				config.Store = validClusterStoreConfig()
			}
			err = validateDeploymentConfig(
				&config,
				test.clusterSelfOverride,
				test.pprofURL,
			)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("validateDeploymentConfig() 返回意外错误：%v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("validateDeploymentConfig() 错误 = %v，期望包含 %q", err, test.wantError)
			}
		})
	}
}

// validClusterStoreConfig 返回支持数据库原子 fencing 的最小 PostgreSQL 配置。
func validClusterStoreConfig() json.RawMessage {
	return json.RawMessage(`{"use_adapter":"postgres","adapters":{"postgres":{}}}`)
}

// validClusterConfig 返回可通过生产部署门禁的三节点静态集群配置。
func validClusterConfig() clusterConfig {
	return clusterConfig{
		ClusterID:        "im-production",
		ExpectedReplicas: 3,
		AdvertiseAddr:    "im-0:12000",
		ThisName:         "im-0",
		ControlPlane: &clusterControlPlaneConfig{
			Provider: "etcd",
			Endpoints: []string{
				"https://etcd-0:2379",
				"https://etcd-1:2379",
				"https://etcd-2:2379",
			},
			Namespace:   "/im/production",
			LeaseTTL:    "10s",
			DialTimeout: "5s",
			TLS: &clusterControlPlaneTLSConfig{
				CAFile:   "/run/secrets/etcd/ca.pem",
				CertFile: "/run/secrets/etcd/client.pem",
				KeyFile:  "/run/secrets/etcd/client-key.pem",
			},
		},
		Transport: &clusterTransportConfig{
			LaneCount:              8,
			ReliableQueueCapacity:  512,
			EphemeralQueueCapacity: 128,
			DialTimeout:            "3s",
			RequestTimeout:         "5s",
		},
		TLS: &clusterTLSConfig{
			CAFile:   "/run/secrets/cluster-ca.pem",
			CertFile: "/run/secrets/cluster-cert.pem",
			KeyFile:  "/run/secrets/cluster-key.pem",
		},
		Nodes: []clusterNodeConfig{
			{Name: "im-0", Addr: "im-0:12000"},
			{Name: "im-1", Addr: "im-1:12000"},
			{Name: "im-2", Addr: "im-2:12000"},
		},
		Failover: &clusterFailoverConfig{
			Enabled:       true,
			Heartbeat:     100,
			VoteAfter:     8,
			NodeFailAfter: 16,
		},
	}
}
