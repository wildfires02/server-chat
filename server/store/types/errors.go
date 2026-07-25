package types

// StoreError 满足 error 接口，并允许使用常量值进行直接相等性比较。
type StoreError string

// Error 实现 error 接口方法。
func (s StoreError) Error() string {
	return string(s)
}

const (
	// ErrInternal 表示数据库或其它内部系统故障。
	ErrInternal = StoreError("internal")
	// ErrMalformed 表示密钥凭据无法解析或格式错误。
	ErrMalformed = StoreError("malformed")
	// ErrFailed 表示认证失败（如用户名或密码错误等）。
	ErrFailed = StoreError("failed")
	// ErrDuplicate 表示凭据重复（如登录名已存在）。
	ErrDuplicate = StoreError("duplicate value")
	// ErrUnsupported 表示不支持该操作。
	ErrUnsupported = StoreError("unsupported")
	// ErrExpired 表示凭据已过期。
	ErrExpired = StoreError("expired")
	// ErrPolicy 表示违反策略规定（如密码强度不够等）。
	ErrPolicy = StoreError("policy")
	// ErrCredentials 表示凭据（如邮箱或验证码）必须先完成校验。
	ErrCredentials = StoreError("credentials")
	// ErrUserNotFound 表示指定的未找到用户。
	ErrUserNotFound = StoreError("user not found")
	// ErrTopicNotFound 表示指定的未找到 Topic 主题。
	ErrTopicNotFound = StoreError("topic not found")
	// ErrNotFound 表示除用户或 Topic 之外的对象未找到。
	ErrNotFound = StoreError("not found")
	// ErrPermissionDenied 表示权限不足，拒绝操作。
	ErrPermissionDenied = StoreError("denied")
	// ErrInvalidResponse 表示客户端响应不符合服务端预期。
	ErrInvalidResponse = StoreError("invalid response")
	// ErrRedirected 表示订阅请求已被重定向到另一个 Topic。
	ErrRedirected = StoreError("redirected")
)
