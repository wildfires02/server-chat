package server

import (
	"chat/api/pbx"
	"chat/server/store/types"
)

func pbAssetPackSerialize(in types.AssetPack) *pbx.AssetPack {
	return &pbx.AssetPack{
		Id:          in.Id,
		Name:        in.Name,
		Description: in.Description,
		Cover:       in.Cover,
		Published:   in.Published,
		CreatedAt:   timeToInt64(&in.CreatedAt),
		UpdatedAt:   timeToInt64(&in.UpdatedAt),
		Version:     in.Version,
	}
}

func pbAssetPackDeserialize(in *pbx.AssetPack) *types.AssetPack {
	if in == nil {
		return nil
	}
	out := &types.AssetPack{
		Id:          in.GetId(),
		Name:        in.GetName(),
		Description: in.GetDescription(),
		Cover:       in.GetCover(),
		Published:   in.GetPublished(),
		Version:     in.GetVersion(),
	}
	if ts := int64ToTime(in.GetCreatedAt()); ts != nil {
		out.CreatedAt = *ts
	}
	if ts := int64ToTime(in.GetUpdatedAt()); ts != nil {
		out.UpdatedAt = *ts
	}
	return out
}

func pbMediaAssetSerialize(in types.MediaAsset) *pbx.MediaAsset {
	out := &pbx.MediaAsset{
		Id:        in.Id,
		PackId:    in.PackId,
		Kind:      in.Kind,
		Url:       in.URL,
		Preview:   in.Preview,
		MimeType:  in.MimeType,
		Width:     int32(in.Width),
		Height:    int32(in.Height),
		Duration:  int32(in.Duration),
		Keywords:  in.Keywords,
		Published: in.Published,
		CreatedAt: timeToInt64(&in.CreatedAt),
		UpdatedAt: timeToInt64(&in.UpdatedAt),
		Version:   in.Version,
		Alt:       in.Alt,
		Sha256:    in.SHA256,
		Size:      in.Size,
		Revision:  in.Revision,
	}
	for _, variant := range in.Variants {
		out.Variants = append(out.Variants, &pbx.AssetVariant{
			Name:     variant.Name,
			Url:      variant.URL,
			MimeType: variant.MimeType,
			Width:    int32(variant.Width),
			Height:   int32(variant.Height),
			Size:     variant.Size,
			Sha256:   variant.SHA256,
		})
	}
	return out
}

func pbMediaAssetDeserialize(in *pbx.MediaAsset) *types.MediaAsset {
	if in == nil {
		return nil
	}
	out := &types.MediaAsset{
		Id:        in.GetId(),
		PackId:    in.GetPackId(),
		Kind:      in.GetKind(),
		URL:       in.GetUrl(),
		Preview:   in.GetPreview(),
		MimeType:  in.GetMimeType(),
		Width:     int(in.GetWidth()),
		Height:    int(in.GetHeight()),
		Duration:  int(in.GetDuration()),
		Keywords:  in.GetKeywords(),
		Published: in.GetPublished(),
		Version:   in.GetVersion(),
		Alt:       in.GetAlt(),
		SHA256:    in.GetSha256(),
		Size:      in.GetSize(),
		Revision:  in.GetRevision(),
	}
	for _, variant := range in.GetVariants() {
		out.Variants = append(out.Variants, types.AssetVariant{
			Name:     variant.GetName(),
			URL:      variant.GetUrl(),
			MimeType: variant.GetMimeType(),
			Width:    int(variant.GetWidth()),
			Height:   int(variant.GetHeight()),
			Size:     variant.GetSize(),
			SHA256:   variant.GetSha256(),
		})
	}
	if ts := int64ToTime(in.GetCreatedAt()); ts != nil {
		out.CreatedAt = *ts
	}
	if ts := int64ToTime(in.GetUpdatedAt()); ts != nil {
		out.UpdatedAt = *ts
	}
	return out
}

func pbAssetMutationSerialize(in *types.AssetMutation) *pbx.AssetMutation {
	if in == nil {
		return nil
	}
	out := &pbx.AssetMutation{
		Op:      in.Op,
		PackId:  in.PackId,
		AssetId: in.AssetId,
	}
	if in.Pack != nil {
		out.Pack = pbAssetPackSerialize(*in.Pack)
	}
	if in.Asset != nil {
		out.Asset = pbMediaAssetSerialize(*in.Asset)
	}
	return out
}

func pbAssetMutationDeserialize(in *pbx.AssetMutation) *types.AssetMutation {
	if in == nil {
		return nil
	}
	return &types.AssetMutation{
		Op:      in.GetOp(),
		Pack:    pbAssetPackDeserialize(in.GetPack()),
		Asset:   pbMediaAssetDeserialize(in.GetAsset()),
		PackId:  in.GetPackId(),
		AssetId: in.GetAssetId(),
	}
}

func pbAssetCatalogSerialize(in *types.AssetCatalog) *pbx.AssetCatalog {
	if in == nil {
		return nil
	}
	out := &pbx.AssetCatalog{Version: in.Version}
	for _, pack := range in.Packs {
		out.Packs = append(out.Packs, pbAssetPackSerialize(pack))
	}
	for _, asset := range in.Assets {
		out.Assets = append(out.Assets, pbMediaAssetSerialize(asset))
	}
	return out
}

func pbAssetCatalogDeserialize(in *pbx.AssetCatalog) *types.AssetCatalog {
	if in == nil {
		return nil
	}
	out := &types.AssetCatalog{Version: in.GetVersion()}
	for _, pack := range in.GetPacks() {
		if converted := pbAssetPackDeserialize(pack); converted != nil {
			out.Packs = append(out.Packs, *converted)
		}
	}
	for _, asset := range in.GetAssets() {
		if converted := pbMediaAssetDeserialize(asset); converted != nil {
			out.Assets = append(out.Assets, *converted)
		}
	}
	return out
}
