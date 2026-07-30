package types

import "time"

// AssetPack 是一组可发布的贴纸、动态 Emoji 或 GIF。
type AssetPack struct {
	Id          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Cover       string    `json:"cover,omitempty"`
	Published   bool      `json:"published"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Version     uint64    `json:"version"`
}

// AssetVariant 是同一逻辑素材的一个传输规格。消息仍只保存 MediaAsset.Id，
// 客户端按能力选择 WebP、WebM、Lottie 等规格并按 revision 缓存。
type AssetVariant struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	MimeType string `json:"mime"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

// MediaAsset 是消息正文引用的服务端素材。
type MediaAsset struct {
	Id        string         `json:"id"`
	PackId    string         `json:"pack_id"`
	Kind      string         `json:"kind"`
	URL       string         `json:"url"`
	Preview   string         `json:"preview,omitempty"`
	MimeType  string         `json:"mime,omitempty"`
	Width     int            `json:"width,omitempty"`
	Height    int            `json:"height,omitempty"`
	Duration  int            `json:"duration,omitempty"`
	Keywords  []string       `json:"keywords,omitempty"`
	Published bool           `json:"published"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	Version   uint64         `json:"version"`
	Alt       string         `json:"alt,omitempty"`
	SHA256    string         `json:"sha256"`
	Size      int64          `json:"size"`
	Revision  uint64         `json:"revision"`
	Variants  []AssetVariant `json:"variants,omitempty"`
}

// AssetMutation 表示素材包或素材的管理操作。
type AssetMutation struct {
	Op      string      `json:"op"`
	Pack    *AssetPack  `json:"pack,omitempty"`
	Asset   *MediaAsset `json:"asset,omitempty"`
	PackId  string      `json:"pack_id,omitempty"`
	AssetId string      `json:"asset_id,omitempty"`
}

// AssetQuery 是素材目录查询。
type AssetQuery struct {
	PackId   string   `json:"pack_id,omitempty"`
	Query    string   `json:"q,omitempty"`
	Kind     string   `json:"kind,omitempty"`
	Since    uint64   `json:"since,omitempty"`
	Limit    int      `json:"limit,omitempty"`
	AssetIds []string `json:"asset_ids,omitempty"`
}

// AssetCatalog 是素材目录查询结果。
type AssetCatalog struct {
	Version uint64       `json:"version"`
	Packs   []AssetPack  `json:"packs,omitempty"`
	Assets  []MediaAsset `json:"assets,omitempty"`
}
