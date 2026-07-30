package admin

import "sort"

var permissionCatalog = []Permission{
	{Key: "system.settings.read", Group: "系统", Name: "查看基础配置", Description: "查看产品策略和只读运行参数", Object: "im:system:settings", Action: "read", Risk: "low", Stage: "ready"},
	{Key: "system.settings.write", Group: "系统", Name: "修改基础配置", Description: "修改产品策略、默认语言和安全限制", Object: "im:system:settings", Action: "write", Risk: "high", Stage: "ready"},
	{Key: "system.roles.read", Group: "系统", Name: "查看角色权限", Description: "查看权限目录、角色和主体绑定", Object: "im:system:roles", Action: "read", Risk: "low", Stage: "ready"},
	{Key: "system.roles.write", Group: "系统", Name: "修改角色权限", Description: "创建角色、分配权限和维护主体绑定", Object: "im:system:roles", Action: "write", Risk: "critical", Stage: "ready"},
	{Key: "system.audit.read", Group: "系统", Name: "查看操作审计", Description: "查看后台配置变更记录", Object: "im:system:audit", Action: "read", Risk: "medium", Stage: "ready"},
	{Key: "official_topics.read", Group: "官方会话", Name: "查看官方频道和大群", Description: "查看官方频道、大群及其状态", Object: "im:official-topic", Action: "read", Risk: "low", Stage: "foundation"},
	{Key: "official_topics.create", Group: "官方会话", Name: "创建官方频道和大群", Description: "创建平台认证的只读频道或官方大群", Object: "im:official-topic", Action: "create", Risk: "high", Stage: "foundation"},
	{Key: "official_topics.manage", Group: "官方会话", Name: "管理官方频道和大群", Description: "修改资料、发布者及群组安全策略", Object: "im:official-topic", Action: "manage", Risk: "high", Stage: "foundation"},
	{Key: "official_topics.publish", Group: "官方会话", Name: "发布官方消息", Description: "代表官方频道发布或撤回内容", Object: "im:official-topic", Action: "publish", Risk: "high", Stage: "foundation"},
	{Key: "moderation.read", Group: "群组治理", Name: "查看治理记录", Description: "查看禁言、移出和封禁记录", Object: "im:moderation", Action: "read", Risk: "medium", Stage: "foundation"},
	{Key: "moderation.mute", Group: "群组治理", Name: "禁言成员", Description: "设置或解除成员定时禁言", Object: "im:moderation", Action: "mute", Risk: "high", Stage: "foundation"},
	{Key: "moderation.remove", Group: "群组治理", Name: "移出成员", Description: "从群组移出成员并记录原因", Object: "im:moderation", Action: "remove", Risk: "high", Stage: "foundation"},
	{Key: "moderation.ban", Group: "群组治理", Name: "封禁成员", Description: "阻止成员重新加入指定群组", Object: "im:moderation", Action: "ban", Risk: "critical", Stage: "foundation"},
	{Key: "assets.read", Group: "素材", Name: "查看素材", Description: "查看贴纸、动态 Emoji 和 GIF 目录", Object: "im:asset", Action: "read", Risk: "low", Stage: "ready"},
	{Key: "assets.write", Group: "素材", Name: "维护素材", Description: "创建素材包并维护素材元数据", Object: "im:asset", Action: "write", Risk: "medium", Stage: "ready"},
	{Key: "assets.publish", Group: "素材", Name: "发布素材", Description: "发布或下架客户端可见素材", Object: "im:asset", Action: "publish", Risk: "high", Stage: "ready"},
	{Key: "contacts.read", Group: "客户协作", Name: "查看联系人", Description: "查看联系人、分组和好友关系", Object: "im:contact", Action: "read", Risk: "medium", Stage: "ready"},
	{Key: "contacts.manage", Group: "客户协作", Name: "管理联系人", Description: "维护联系人和好友关系", Object: "im:contact", Action: "manage", Risk: "high", Stage: "ready"},
	{Key: "workspace.pins.read", Group: "客户协作", Name: "查看内部置顶", Description: "查看员工个人的客户、会话和重点消息置顶", Object: "im:workspace-pin", Action: "read", Risk: "medium", Stage: "foundation"},
	{Key: "workspace.pins.write", Group: "客户协作", Name: "维护内部置顶", Description: "创建、排序和取消员工个人的内部置顶", Object: "im:workspace-pin", Action: "write", Risk: "medium", Stage: "foundation"},
	{Key: "translation.manage", Group: "翻译", Name: "管理翻译策略", Description: "配置员工与客户的消息翻译方向", Object: "im:translation", Action: "manage", Risk: "high", Stage: "foundation"},
	{Key: "notifications.manage", Group: "通知", Name: "管理通知策略", Description: "配置推送和安静时段默认值", Object: "im:notification", Action: "manage", Risk: "medium", Stage: "foundation"},
	{Key: "payments.read", Group: "资金", Name: "查看红包与转账", Description: "查看聊天资金卡片与对账状态", Object: "im:payment", Action: "read", Risk: "high", Stage: "integration"},
	{Key: "payments.send", Group: "资金", Name: "发送红包与转账", Description: "向授权客户发起红包或转账", Object: "im:payment", Action: "send", Risk: "critical", Stage: "integration"},
	{Key: "payments.approve", Group: "资金", Name: "审批红包与转账", Description: "审批超额或高风险资金订单", Object: "im:payment", Action: "approve", Risk: "critical", Stage: "integration"},
}

// PermissionCatalog 按稳定展示顺序返回防御性副本。
func PermissionCatalog() []Permission {
	out := append([]Permission(nil), permissionCatalog...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Group == out[j].Group {
			return out[i].Key < out[j].Key
		}
		return out[i].Group < out[j].Group
	})
	return out
}

func permissionByKey(key string) (Permission, bool) {
	for _, permission := range permissionCatalog {
		if permission.Key == key {
			return permission, true
		}
	}
	return Permission{}, false
}

func permissionKeys(stage string) []string {
	out := make([]string, 0, len(permissionCatalog))
	for _, permission := range permissionCatalog {
		if stage == "" || permission.Stage == stage {
			out = append(out, permission.Key)
		}
	}
	sort.Strings(out)
	return out
}
