package store

import "chat/server/store/types"

// DevicePersistenceInterface 定义处理设备 ID 的方法接口。
// 主要用于生成推送通知。
type DevicePersistenceInterface interface {
	Update(uid types.Uid, oldDeviceID string, dev *types.DeviceDef) error
	GetAll(uid ...types.Uid) (map[types.Uid][]types.DeviceDef, int, error)
	Delete(uid types.Uid, deviceID string) error
}

// deviceMapper 是实现 DevicePersistenceInterface 的具体类型。
type deviceMapper struct{}

// Devices 是 DevicePersistenceInterface 的单例实例，用于映射方法。
var Devices DevicePersistenceInterface

// Update 更新设备记录。
func (deviceMapper) Update(uid types.Uid, oldDeviceID string, dev *types.DeviceDef) error {
	// 如果指定了旧设备 ID 且与新 ID 不同，删除旧 ID
	if oldDeviceID != "" && (dev == nil || dev.DeviceId != oldDeviceID) {
		if err := adp.DeviceDelete(uid, oldDeviceID); err != nil {
			return err
		}
	}

	// 如果提供了新 DeviceId，则插入或更新。
	if dev != nil && dev.DeviceId != "" {
		return adp.DeviceUpsert(uid, dev)
	}
	return nil
}

// GetAll 返回给定用户 ID 列表的所有已知设备 ID。
// 第二个返回参数是找到的设备 ID 计数。
func (deviceMapper) GetAll(uid ...types.Uid) (map[types.Uid][]types.DeviceDef, int, error) {
	return adp.DeviceGetAll(uid...)
}

// Delete 删除给定用户的设备记录。
func (deviceMapper) Delete(uid types.Uid, deviceID string) error {
	return adp.DeviceDelete(uid, deviceID)
}
