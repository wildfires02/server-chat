package store

import (
	"encoding/hex"
	"errors"
	"net/url"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"chat/server/store/types"
)

const assetCatalogKey = "assets:catalog"

const (
	maxAssetQueryIDs = 200
	maxAssetVariants = 8
)

type assetCatalogState struct {
	Version uint64                      `json:"version"`
	Packs   map[string]types.AssetPack  `json:"packs"`
	Assets  map[string]types.MediaAsset `json:"assets"`
}

// AssetPersistenceInterface 管理贴纸、动态 Emoji 与 GIF 素材目录。
type AssetPersistenceInterface interface {
	Query(query types.AssetQuery, includeUnpublished bool) (*types.AssetCatalog, error)
	Apply(mutation types.AssetMutation) (*types.AssetCatalog, error)
	Validate(kind string, ids []string) error
}

type assetMapper struct {
	mu sync.Mutex
}

// Assets 是素材目录持久化入口。
var Assets AssetPersistenceInterface

func emptyAssetCatalog() *assetCatalogState {
	return &assetCatalogState{
		Packs:  make(map[string]types.AssetPack),
		Assets: make(map[string]types.MediaAsset),
	}
}

func loadAssetCatalog() (*assetCatalogState, error) {
	raw, err := PCache.Get(assetCatalogKey)
	if errors.Is(err, types.ErrNotFound) {
		return emptyAssetCatalog(), nil
	}
	if err != nil {
		return nil, err
	}
	state := emptyAssetCatalog()
	if err = unmarshalPersistentJSON(raw, state); err != nil {
		return nil, err
	}
	if state.Packs == nil {
		state.Packs = make(map[string]types.AssetPack)
	}
	if state.Assets == nil {
		state.Assets = make(map[string]types.MediaAsset)
	}
	return state, nil
}

func saveAssetCatalog(state *assetCatalogState) error {
	raw, err := marshalPersistentJSON(state)
	if err != nil {
		return err
	}
	return PCache.Upsert(assetCatalogKey, raw, false)
}

func validAssetID(id string) bool {
	return validContactGroupID(id) && len(id) <= 64
}

func validAssetKind(kind string) bool {
	return kind == "sticker" || kind == "animated-emoji" || kind == "gif"
}

func validAssetURL(raw string) bool {
	if raw == "" {
		return true
	}
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		return true
	}
	parsed, err := url.ParseRequestURI(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != ""
}

func normalizeSHA256(raw string) (string, bool) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	decoded, err := hex.DecodeString(raw)
	return raw, err == nil && len(decoded) == 32
}

func normalizeAsset(asset *types.MediaAsset) error {
	if asset == nil || !validAssetID(asset.Id) || !validAssetID(asset.PackId) ||
		!validAssetKind(asset.Kind) || asset.URL == "" ||
		len(asset.URL) > 4096 || len(asset.Preview) > 4096 ||
		len(asset.MimeType) > 255 || len(asset.Keywords) > 32 ||
		len(asset.Alt) > 64 || len(asset.Variants) > maxAssetVariants ||
		asset.Width < 0 || asset.Height < 0 || asset.Duration < 0 || asset.Size < 0 ||
		!utf8.ValidString(asset.Alt) || !validAssetURL(asset.URL) ||
		!validAssetURL(asset.Preview) {
		return types.ErrMalformed
	}
	var ok bool
	if asset.SHA256, ok = normalizeSHA256(asset.SHA256); !ok {
		return types.ErrMalformed
	}

	seenKeywords := make(map[string]struct{}, len(asset.Keywords))
	keywords := make([]string, 0, len(asset.Keywords))
	for _, keyword := range asset.Keywords {
		keyword = strings.TrimSpace(keyword)
		key := strings.ToLower(keyword)
		if keyword == "" || len(keyword) > 128 || !utf8.ValidString(keyword) {
			return types.ErrMalformed
		}
		if _, exists := seenKeywords[key]; !exists {
			seenKeywords[key] = struct{}{}
			keywords = append(keywords, keyword)
		}
	}
	sort.Slice(keywords, func(i, j int) bool {
		return strings.ToLower(keywords[i]) < strings.ToLower(keywords[j])
	})
	asset.Keywords = keywords

	seenVariants := make(map[string]struct{}, len(asset.Variants))
	for idx := range asset.Variants {
		variant := &asset.Variants[idx]
		variant.Name = strings.TrimSpace(variant.Name)
		variant.MimeType = strings.TrimSpace(variant.MimeType)
		if !validAssetID(variant.Name) || variant.URL == "" ||
			len(variant.URL) > 4096 || len(variant.MimeType) > 255 ||
			variant.Width < 0 || variant.Height < 0 || variant.Size < 0 ||
			!validAssetURL(variant.URL) {
			return types.ErrMalformed
		}
		if _, exists := seenVariants[variant.Name]; exists {
			return types.ErrMalformed
		}
		seenVariants[variant.Name] = struct{}{}
		if variant.SHA256, ok = normalizeSHA256(variant.SHA256); !ok {
			return types.ErrMalformed
		}
	}
	sort.Slice(asset.Variants, func(i, j int) bool {
		return asset.Variants[i].Name < asset.Variants[j].Name
	})
	return nil
}

func assetPayloadEqual(left, right types.MediaAsset) bool {
	if left.PackId != right.PackId || left.Kind != right.Kind || left.URL != right.URL ||
		left.Preview != right.Preview || left.MimeType != right.MimeType ||
		left.Width != right.Width || left.Height != right.Height ||
		left.Duration != right.Duration || left.SHA256 != right.SHA256 ||
		left.Size != right.Size || len(left.Variants) != len(right.Variants) {
		return false
	}
	for idx := range left.Variants {
		if left.Variants[idx] != right.Variants[idx] {
			return false
		}
	}
	return true
}

func assetPublicURLs(asset types.MediaAsset) []string {
	urls := []string{asset.URL}
	if asset.Preview != "" {
		urls = append(urls, asset.Preview)
	}
	for _, variant := range asset.Variants {
		urls = append(urls, variant.URL)
	}
	return urls
}

func setAssetPublicUpdates(updates map[string]bool, asset types.MediaAsset, public bool) {
	for _, rawURL := range assetPublicURLs(asset) {
		if rawURL != "" {
			updates[rawURL] = public
		}
	}
}

func populateAssetFileMetadata(rawURL string, mimeType *string, size *int64, sha256 *string) error {
	fid := localFileID(rawURL)
	if fid == "" {
		return nil
	}
	definition, err := Files.Get(fid)
	if err != nil {
		return err
	}
	if definition == nil {
		return types.ErrNotFound
	}
	if *mimeType == "" {
		*mimeType = definition.MimeType
	} else if definition.MimeType != "" && *mimeType != definition.MimeType {
		return types.ErrPolicy
	}
	if *size == 0 {
		*size = definition.Size
	} else if definition.Size > 0 && *size != definition.Size {
		return types.ErrPolicy
	}
	state, err := GetFileProcessingState(fid)
	if err != nil {
		return err
	}
	if *sha256 == "" {
		*sha256 = state.SHA256
	} else if normalized, valid := normalizeSHA256(state.SHA256); valid &&
		!strings.EqualFold(*sha256, normalized) {
		return types.ErrPolicy
	}
	return nil
}

// 填充媒体资产元数据将受信任的上传记录用于本地文件。 外在的
//CDN资产必须在根突变中提供其摘要和元数据。
func PopulateMediaAssetMetadata(asset *types.MediaAsset) error {
	if asset == nil {
		return types.ErrMalformed
	}
	if err := populateAssetFileMetadata(asset.URL, &asset.MimeType, &asset.Size, &asset.SHA256); err != nil {
		return err
	}
	for idx := range asset.Variants {
		variant := &asset.Variants[idx]
		if err := populateAssetFileMetadata(variant.URL, &variant.MimeType,
			&variant.Size, &variant.SHA256); err != nil {
			return err
		}
	}
	return nil
}

// Apply 新增、修改或删除素材包和素材。
func (m *assetMapper) Apply(mutation types.AssetMutation) (*types.AssetCatalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, err := loadAssetCatalog()
	if err != nil {
		return nil, err
	}
	now := types.TimeNow()
	state.Version++
	var publicUpdates = make(map[string]bool)
	switch strings.ToLower(mutation.Op) {
	case "upsert_pack":
		if mutation.Pack == nil || !validAssetID(mutation.Pack.Id) ||
			strings.TrimSpace(mutation.Pack.Name) == "" || len(mutation.Pack.Name) > 256 ||
			len(mutation.Pack.Description) > 2048 || len(mutation.Pack.Cover) > 4096 ||
			!utf8.ValidString(mutation.Pack.Name) || !utf8.ValidString(mutation.Pack.Description) ||
			!validAssetURL(mutation.Pack.Cover) {
			return nil, types.ErrMalformed
		}
		pack := *mutation.Pack
		if old, ok := state.Packs[pack.Id]; ok {
			pack.CreatedAt = old.CreatedAt
			if old.Cover != "" && old.Cover != pack.Cover {
				publicUpdates[old.Cover] = false
			}
		} else {
			pack.CreatedAt = now
		}
		pack.Name = strings.TrimSpace(pack.Name)
		pack.UpdatedAt = now
		pack.Version = state.Version
		state.Packs[pack.Id] = pack
		if pack.Cover != "" {
			publicUpdates[pack.Cover] = pack.Published
		}
		for _, asset := range state.Assets {
			if asset.PackId == pack.Id {
				setAssetPublicUpdates(publicUpdates, asset, pack.Published && asset.Published)
			}
		}
	case "delete_pack":
		pack, ok := state.Packs[mutation.PackId]
		if !ok {
			return nil, types.ErrNotFound
		}
		if pack.Cover != "" {
			publicUpdates[pack.Cover] = false
		}
		pack.Published = false
		pack.UpdatedAt = now
		pack.Version = state.Version
		state.Packs[mutation.PackId] = pack
		for id, asset := range state.Assets {
			if asset.PackId == mutation.PackId {
				setAssetPublicUpdates(publicUpdates, asset, false)
				asset.Published = false
				asset.UpdatedAt = now
				asset.Version = state.Version
				state.Assets[id] = asset
			}
		}
	case "upsert_asset":
		if err = normalizeAsset(mutation.Asset); err != nil {
			return nil, err
		}
		if _, ok := state.Packs[mutation.Asset.PackId]; !ok {
			return nil, types.ErrNotFound
		}
		asset := *mutation.Asset
		if old, ok := state.Assets[asset.Id]; ok {
			asset.CreatedAt = old.CreatedAt
			if old.SHA256 == "" {
				old.SHA256 = asset.SHA256
			}
			if old.Size == 0 {
				old.Size = asset.Size
			}
			if len(old.Variants) == 0 {
				old.Variants = append([]types.AssetVariant(nil), asset.Variants...)
			}
			if !assetPayloadEqual(old, asset) {
				return nil, types.ErrPolicy
			}
			asset.Revision = old.Revision
			if asset.Revision == 0 {
				asset.Revision = 1
			}
		} else {
			asset.CreatedAt = now
			asset.Revision = 1
		}
		asset.UpdatedAt = now
		asset.Version = state.Version
		state.Assets[asset.Id] = asset
		pack := state.Packs[asset.PackId]
		setAssetPublicUpdates(publicUpdates, asset, pack.Published && asset.Published)
	case "delete_asset":
		asset, ok := state.Assets[mutation.AssetId]
		if !ok {
			return nil, types.ErrNotFound
		}
		setAssetPublicUpdates(publicUpdates, asset, false)
		asset.Published = false
		asset.UpdatedAt = now
		asset.Version = state.Version
		state.Assets[mutation.AssetId] = asset
	default:
		return nil, types.ErrMalformed
	}
	if err = saveAssetCatalog(state); err != nil {
		return nil, err
	}
	for rawURL, public := range publicUpdates {
		if err = SetFilePublicAccess(rawURL, public); err != nil {
			return nil, err
		}
	}
	return assetCatalogResult(state, types.AssetQuery{}, true), nil
}

// Query 搜索已发布的素材目录；管理员可请求未发布内容。
func (m *assetMapper) Query(query types.AssetQuery, includeUnpublished bool) (*types.AssetCatalog, error) {
	if query.Limit < 0 || query.Limit > 1000 ||
		(query.Kind != "" && !validAssetKind(query.Kind)) ||
		len(query.AssetIds) > maxAssetQueryIDs {
		return nil, types.ErrMalformed
	}
	for _, id := range query.AssetIds {
		if !validAssetID(id) {
			return nil, types.ErrMalformed
		}
	}
	m.mu.Lock()
	state, err := loadAssetCatalog()
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return assetCatalogResult(state, query, includeUnpublished), nil
}

func assetCatalogResult(state *assetCatalogState, query types.AssetQuery, includeUnpublished bool) *types.AssetCatalog {
	out := &types.AssetCatalog{Version: state.Version}
	if len(query.AssetIds) == 0 && query.Since >= state.Version && query.Since != 0 {
		return out
	}
	exactIDs := make(map[string]struct{}, len(query.AssetIds))
	for _, id := range query.AssetIds {
		exactIDs[id] = struct{}{}
	}
	needle := strings.ToLower(strings.TrimSpace(query.Query))
	matchedPacks := make(map[string]struct{})
	for _, asset := range state.Assets {
		pack, exists := state.Packs[asset.PackId]
		if !exists ||
			(!includeUnpublished && (!asset.Published || !pack.Published)) ||
			(query.PackId != "" && asset.PackId != query.PackId) ||
			(query.Kind != "" && asset.Kind != query.Kind) {
			continue
		}
		if len(exactIDs) > 0 {
			if _, exists = exactIDs[asset.Id]; !exists {
				continue
			}
		}
		if needle != "" {
			haystack := strings.ToLower(asset.Id + " " + strings.Join(asset.Keywords, " "))
			if !strings.Contains(haystack, needle) {
				continue
			}
		}
		out.Assets = append(out.Assets, asset)
		matchedPacks[asset.PackId] = struct{}{}
	}
	for _, pack := range state.Packs {
		if !includeUnpublished && !pack.Published {
			continue
		}
		if len(exactIDs) > 0 {
			if _, exists := matchedPacks[pack.Id]; !exists {
				continue
			}
		}
		out.Packs = append(out.Packs, pack)
	}
	sort.Slice(out.Packs, func(i, j int) bool { return out.Packs[i].Id < out.Packs[j].Id })
	sort.Slice(out.Assets, func(i, j int) bool { return out.Assets[i].Id < out.Assets[j].Id })
	if query.Limit > 0 && len(out.Assets) > query.Limit {
		out.Assets = out.Assets[:query.Limit]
	}
	return out
}

// Validate 确认消息引用的素材存在、已发布且类型匹配。
func (m *assetMapper) Validate(kind string, ids []string) error {
	if len(ids) == 0 {
		if validAssetKind(kind) {
			return types.ErrMalformed
		}
		return nil
	}
	m.mu.Lock()
	state, err := loadAssetCatalog()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	for _, id := range ids {
		asset, ok := state.Assets[id]
		pack, packOK := state.Packs[asset.PackId]
		if !ok || !packOK || !asset.Published || !pack.Published || asset.Kind != kind {
			return types.ErrNotFound
		}
	}
	return nil
}

// ValidateMessageAssets 是消息发布层使用的素材校验入口。
func ValidateMessageAssets(kind string, ids []string) error {
	return Assets.Validate(kind, ids)
}
