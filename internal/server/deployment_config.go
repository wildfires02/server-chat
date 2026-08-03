// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

const (
	// environmentDevelopment 表示本地开发环境。
	environmentDevelopment = "development"
	// environmentTest 表示自动化测试环境。
	environmentTest = "test"
	// environmentStaging 表示与生产拓扑一致的预发布环境。
	environmentStaging = "staging"
	// environmentProduction 表示正式生产环境。
	environmentProduction = "production"

	// deploymentModeStandalone 表示不初始化集群资源的开发单机模式。
	deploymentModeStandalone = "standalone"
	// deploymentModeCluster 表示启用节点间通信的集群模式。
	deploymentModeCluster = "cluster"
)

// runtimeConfig 保存运行环境和部署模式，避免通过空字段隐式切换单机或集群。
type runtimeConfig struct {
	// Environment 指定 development、test、staging 或 production。
	Environment string `json:"environment"`
	// DeploymentMode 指定 standalone 或 cluster。
	DeploymentMode string `json:"deployment_mode"`
}

// deploymentStoreConfig 只提取部署门禁需要的数据库适配器字段。
type deploymentStoreConfig struct {
	// UseAdapter 是当前进程实际启用的数据库适配器名称。
	UseAdapter string `json:"use_adapter"`
	// Adapters 保存各适配器的原始配置，用于校验事务前置条件。
	Adapters map[string]json.RawMessage `json:"adapters"`
}

// deploymentMongoConfig 保存 MongoDB 集群 fencing 依赖的 Replica Set 配置。
type deploymentMongoConfig struct {
	// ReplicaSet 非空时 MongoDB 适配器才会启用跨文档事务。
	ReplicaSet string `json:"replica_set"`
}

// validateDeploymentConfig 在服务初始化任何外部资源前校验部署安全边界。
func validateDeploymentConfig(config *configType, clusterSelfOverride, pprofURL string) error {
	if config == nil {
		return fmt.Errorf("运行配置不能为空")
	}
	if _, err := normalizeHealthConfig(config.Health); err != nil {
		return err
	}

	environment := strings.ToLower(strings.TrimSpace(config.Runtime.Environment))
	mode := strings.ToLower(strings.TrimSpace(config.Runtime.DeploymentMode))
	if err := validateRuntimeMode(environment, mode); err != nil {
		return err
	}
	config.Runtime.Environment = environment
	config.Runtime.DeploymentMode = mode
	if err := validateClientOrigins(config.ClientOrigins, environment); err != nil {
		return err
	}
	if config.Translation != nil && config.Translation.Enabled &&
		(config.Translation.RefreshInterval < 1 || config.Translation.RefreshInterval > 300) {
		return fmt.Errorf("translation.refresh_interval 必须在 1..300 秒之间")
	}
	if config.BusinessPolicy != nil && config.BusinessPolicy.Enabled {
		if _, err := newBusinessPolicyClient(*config.BusinessPolicy); err != nil {
			return err
		}
	}
	if config.Media != nil && mode == deploymentModeCluster &&
		(environment == environmentStaging || environment == environmentProduction) &&
		strings.EqualFold(strings.TrimSpace(config.Media.UseHandler), "fs") {
		return fmt.Errorf("%s 集群的断点续传必须使用共享媒体存储，不能使用本地 fs handler",
			environment)
	}
	var cluster clusterConfig
	if len(config.Cluster) > 0 {
		if err := json.Unmarshal(config.Cluster, &cluster); err != nil {
			return fmt.Errorf("解析 cluster_config 失败: %w", err)
		}
	}

	effectiveSelf := strings.TrimSpace(clusterSelfOverride)
	if effectiveSelf == "" {
		effectiveSelf = strings.TrimSpace(cluster.ThisName)
	}

	if mode == deploymentModeStandalone {
		if effectiveSelf != "" {
			return fmt.Errorf("standalone 模式不能配置集群节点身份 %q", effectiveSelf)
		}
		return nil
	}

	if err := validateClusterConfig(cluster, effectiveSelf, environment); err != nil {
		return err
	}
	if cluster.ControlPlane != nil {
		if err := validateClusterStorage(config.Store); err != nil {
			return err
		}
	}
	if environment == environmentProduction && strings.TrimSpace(pprofURL) != "" {
		return fmt.Errorf("production 环境禁止通过 pprof_url 暴露性能分析接口")
	}
	return nil
}

func validateClientOrigins(origins []string, environment string) error {
	for index, rawOrigin := range origins {
		origin := strings.TrimRight(strings.TrimSpace(rawOrigin), "/")
		if origin == "" || origin == "*" {
			return fmt.Errorf("client_origins[%d] 必须是明确的浏览器来源，不能使用通配符", index)
		}
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" ||
			parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
			return fmt.Errorf("client_origins[%d] 不是有效的浏览器来源", index)
		}
		if parsed.Scheme != "https" &&
			!((environment == environmentDevelopment || environment == environmentTest) &&
				parsed.Scheme == "http") {
			return fmt.Errorf("client_origins[%d] 必须使用 HTTPS", index)
		}
	}
	return nil
}

// validateClusterStorage 拒绝不能原子校验 fence 与保存消息的数据库配置。
func validateClusterStorage(rawConfig json.RawMessage) error {
	var config deploymentStoreConfig
	if len(rawConfig) == 0 {
		return fmt.Errorf("启用控制面时必须配置 store_config")
	}
	if err := json.Unmarshal(rawConfig, &config); err != nil {
		return fmt.Errorf("解析 store_config 失败: %w", err)
	}
	adapterName := strings.ToLower(strings.TrimSpace(config.UseAdapter))
	switch adapterName {
	case "postgres", "mysql":
		return nil
	case "mongodb":
		var mongo deploymentMongoConfig
		if err := json.Unmarshal(config.Adapters[adapterName], &mongo); err != nil {
			return fmt.Errorf("解析 MongoDB 集群存储配置失败: %w", err)
		}
		if strings.TrimSpace(mongo.ReplicaSet) == "" {
			return fmt.Errorf("集群模式使用 MongoDB 时必须配置 replica_set 以启用事务")
		}
		return nil
	case "rethinkdb":
		return fmt.Errorf("RethinkDB 不支持集群消息所需的跨文档 fencing 事务")
	case "":
		return fmt.Errorf("启用控制面时必须配置 store_config.use_adapter")
	default:
		return fmt.Errorf("数据库适配器 %q 未通过集群 fencing 认证", adapterName)
	}
}

// validateRuntimeMode 校验运行环境与部署模式的合法组合。
func validateRuntimeMode(environment, mode string) error {
	switch environment {
	case environmentDevelopment, environmentTest, environmentStaging, environmentProduction:
	default:
		return fmt.Errorf("runtime.environment 必须是 development、test、staging 或 production")
	}

	switch mode {
	case deploymentModeStandalone, deploymentModeCluster:
	default:
		return fmt.Errorf("runtime.deployment_mode 必须是 standalone 或 cluster")
	}

	if (environment == environmentStaging || environment == environmentProduction) &&
		mode != deploymentModeCluster {
		return fmt.Errorf("%s 环境必须使用 cluster 部署模式", environment)
	}
	return nil
}

// validateClusterConfig 校验集群节点、身份和生产故障转移配置。
func validateClusterConfig(config clusterConfig, effectiveSelf, environment string) error {
	if effectiveSelf == "" {
		return fmt.Errorf("cluster 模式必须配置 cluster_config.self 或 cluster_self")
	}
	if len(config.Nodes) < 2 {
		return fmt.Errorf("cluster 模式至少需要配置两个节点")
	}

	nodeNames := make(map[string]struct{}, len(config.Nodes))
	nodeAddresses := make(map[string]struct{}, len(config.Nodes))
	selfFound := false
	selfAddress := ""
	for index, node := range config.Nodes {
		name := strings.TrimSpace(node.Name)
		address := strings.TrimSpace(node.Addr)
		if name == "" {
			return fmt.Errorf("cluster_config.nodes[%d].name 不能为空", index)
		}
		if !isValidClusterIdentifier(name) {
			return fmt.Errorf("cluster_config.nodes[%d].name 包含非法字符", index)
		}
		if address == "" {
			return fmt.Errorf("cluster_config.nodes[%d].addr 不能为空", index)
		}
		if _, exists := nodeNames[name]; exists {
			return fmt.Errorf("集群节点名称 %q 重复", name)
		}
		if _, exists := nodeAddresses[address]; exists {
			return fmt.Errorf("集群节点地址 %q 重复", address)
		}
		nodeNames[name] = struct{}{}
		nodeAddresses[address] = struct{}{}
		if name == effectiveSelf {
			selfFound = true
			selfAddress = address
		}
	}
	if !selfFound {
		return fmt.Errorf("当前节点 %q 不在 cluster_config.nodes 中", effectiveSelf)
	}

	if config.ExpectedReplicas != 0 {
		if config.ExpectedReplicas < 3 || config.ExpectedReplicas%2 == 0 {
			return fmt.Errorf("cluster_config.expected_replicas 必须是大于等于 3 的奇数")
		}
		if config.ExpectedReplicas > len(config.Nodes) {
			return fmt.Errorf("cluster_config.expected_replicas=%d 不能大于 nodes 候选节点数量 %d",
				config.ExpectedReplicas, len(config.Nodes))
		}
	}
	initialMembers := normalizedInitialMembers(config)
	if len(initialMembers) != config.ExpectedReplicas {
		return fmt.Errorf(
			"cluster_config.initial_members 数量 %d 与 expected_replicas=%d 不一致",
			len(initialMembers),
			config.ExpectedReplicas,
		)
	}
	initialSet := make(map[string]struct{}, len(initialMembers))
	for index, name := range initialMembers {
		name = strings.TrimSpace(name)
		if _, exists := nodeNames[name]; !exists {
			return fmt.Errorf("cluster_config.initial_members[%d]=%q 不在 nodes 白名单中", index, name)
		}
		if _, duplicated := initialSet[name]; duplicated {
			return fmt.Errorf("cluster_config.initial_members 包含重复节点 %q", name)
		}
		initialSet[name] = struct{}{}
	}

	if config.ControlPlane != nil {
		if strings.TrimSpace(config.ClusterID) == "" {
			return fmt.Errorf("启用控制面时必须配置 cluster_config.cluster_id")
		}
		if !isValidClusterIdentifier(strings.TrimSpace(config.ClusterID)) {
			return fmt.Errorf("cluster_config.cluster_id 包含非法字符")
		}
		if config.ExpectedReplicas == 0 {
			return fmt.Errorf("启用控制面时必须配置 cluster_config.expected_replicas")
		}
		if _, err := normalizeControlPlaneConfig(*config.ControlPlane); err != nil {
			return err
		}
	}
	if config.Transport != nil {
		if _, err := normalizeClusterTransportConfig(*config.Transport); err != nil {
			return err
		}
	}
	if config.TLS != nil {
		if _, err := normalizeClusterTLSConfig(*config.TLS); err != nil {
			return err
		}
	}

	if environment != environmentStaging && environment != environmentProduction {
		return nil
	}
	if strings.TrimSpace(config.ClusterID) == "" {
		return fmt.Errorf("%s 集群必须配置 cluster_config.cluster_id", environment)
	}
	if !isValidClusterIdentifier(strings.TrimSpace(config.ClusterID)) {
		return fmt.Errorf("cluster_config.cluster_id 包含非法字符")
	}
	if config.ExpectedReplicas < 3 || config.ExpectedReplicas%2 == 0 {
		return fmt.Errorf("%s 集群的 expected_replicas 必须是大于等于 3 的奇数", environment)
	}
	if strings.TrimSpace(config.AdvertiseAddr) == "" {
		return fmt.Errorf("%s 集群必须配置 cluster_config.advertise_addr", environment)
	}
	if strings.TrimSpace(config.AdvertiseAddr) != selfAddress {
		return fmt.Errorf("cluster_config.advertise_addr=%q 与当前节点地址 %q 不一致",
			config.AdvertiseAddr, selfAddress)
	}
	if config.ControlPlane == nil {
		return fmt.Errorf("%s 集群必须配置 cluster_config.control_plane", environment)
	}
	if config.Transport == nil {
		return fmt.Errorf("%s 集群必须配置 cluster_config.transport", environment)
	}
	if config.TLS == nil {
		return fmt.Errorf("%s 集群必须配置 cluster_config.tls", environment)
	}
	if len(config.ControlPlane.Endpoints) < 3 {
		return fmt.Errorf("%s 集群至少需要配置 3 个 etcd endpoint", environment)
	}
	if config.ControlPlane.TLS == nil {
		return fmt.Errorf("%s 集群必须配置 cluster_config.control_plane.tls", environment)
	}
	for _, endpoint := range config.ControlPlane.Endpoints {
		if !strings.HasPrefix(strings.ToLower(endpoint), "https://") {
			return fmt.Errorf("%s 集群的 etcd endpoint 必须使用 https", environment)
		}
	}
	return nil
}

// normalizedInitialMembers 返回首次活动成员集合。
// 三节点旧配置未显式声明时使用全部 Nodes，避免开发配置无意义重复；
// 只要 Nodes 多于 expected_replicas，就必须显式声明，防止不同节点因排序
// 或配置漂移创建出不同的初始拓扑。
func normalizedInitialMembers(config clusterConfig) []string {
	if len(config.InitialMembers) > 0 {
		members := make([]string, 0, len(config.InitialMembers))
		for _, name := range config.InitialMembers {
			members = append(members, strings.TrimSpace(name))
		}
		return members
	}
	if config.ExpectedReplicas == len(config.Nodes) {
		members := make([]string, 0, len(config.Nodes))
		for _, node := range config.Nodes {
			members = append(members, strings.TrimSpace(node.Name))
		}
		return members
	}
	return nil
}
