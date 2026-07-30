package store

import (
	"errors"
	"testing"

	"chat/server/store/types"
)

func TestAssetCatalogPublicationAndValidation(t *testing.T) {
	useMemoryPersistentCache(t)
	mapper := &assetMapper{}

	if _, err := mapper.Apply(types.AssetMutation{
		Op: "upsert_pack",
		Pack: &types.AssetPack{
			Id:        "animals",
			Name:      "Animals",
			Published: true,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := mapper.Apply(types.AssetMutation{
		Op: "upsert_asset",
		Asset: &types.MediaAsset{
			Id:        "cat-wave",
			PackId:    "animals",
			Kind:      "sticker",
			URL:       "/v0/file/s/example",
			MimeType:  "image/webp",
			SHA256:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			Size:      1024,
			Published: true,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := mapper.Validate("sticker", []string{"cat-wave"}); err != nil {
		t.Fatalf("published sticker rejected: %v", err)
	}
	if err := mapper.Validate("gif", []string{"cat-wave"}); err != types.ErrNotFound {
		t.Fatalf("kind mismatch: want ErrNotFound, got %v", err)
	}
	result, err := mapper.Query(types.AssetQuery{Kind: "sticker"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Packs) != 1 || len(result.Assets) != 1 || result.Assets[0].Id != "cat-wave" {
		t.Fatalf("unexpected catalog: %#v", result)
	}
}

func TestAssetCatalogExactLookupVariantsAndImmutablePayload(t *testing.T) {
	useMemoryPersistentCache(t)
	mapper := &assetMapper{}
	if _, err := mapper.Apply(types.AssetMutation{
		Op: "upsert_pack",
		Pack: &types.AssetPack{
			Id: "official", Name: "Official", Published: true,
		},
	}); err != nil {
		t.Fatal(err)
	}

	const primaryHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const compactHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	asset := &types.MediaAsset{
		Id: "hello", PackId: "official", Kind: "animated-emoji",
		URL: "https://cdn.example.test/hello.tgs", MimeType: "application/gzip",
		SHA256: primaryHash, Size: 2048, Alt: "👋", Keywords: []string{"Hello", "hello", " wave "},
		Published: true,
		Variants: []types.AssetVariant{{
			Name: "webm", URL: "https://cdn.example.test/hello.webm", MimeType: "video/webm",
			Width: 100, Height: 100, Size: 1024, SHA256: compactHash,
		}},
	}
	if _, err := mapper.Apply(types.AssetMutation{Op: "upsert_asset", Asset: asset}); err != nil {
		t.Fatal(err)
	}

	result, err := mapper.Query(types.AssetQuery{
		AssetIds: []string{"hello"}, Since: ^uint64(0),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Packs) != 1 || len(result.Assets) != 1 {
		t.Fatalf("exact lookup did not return the referenced asset: %#v", result)
	}
	stored := result.Assets[0]
	if stored.Revision != 1 || len(stored.Variants) != 1 ||
		len(stored.Keywords) != 2 || stored.Alt != "👋" {
		t.Fatalf("asset metadata was not normalized: %#v", stored)
	}

	metadataUpdate := stored
	metadataUpdate.Alt = "挥手"
	metadataUpdate.Keywords = []string{"greeting"}
	if _, err = mapper.Apply(types.AssetMutation{Op: "upsert_asset", Asset: &metadataUpdate}); err != nil {
		t.Fatalf("metadata-only update rejected: %v", err)
	}
	replaced := metadataUpdate
	replaced.URL = "https://cdn.example.test/replacement.tgs"
	if _, err = mapper.Apply(types.AssetMutation{Op: "upsert_asset", Asset: &replaced}); !errors.Is(err, types.ErrPolicy) {
		t.Fatalf("content replacement must require a new asset ID, got %v", err)
	}

	if _, err = mapper.Apply(types.AssetMutation{Op: "delete_asset", AssetId: "hello"}); err != nil {
		t.Fatal(err)
	}
	publicResult, err := mapper.Query(types.AssetQuery{AssetIds: []string{"hello"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	rootResult, err := mapper.Query(types.AssetQuery{AssetIds: []string{"hello"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(publicResult.Assets) != 0 || len(rootResult.Assets) != 1 ||
		rootResult.Assets[0].Published {
		t.Fatalf("delete must soft-withdraw the immutable record: public=%#v root=%#v",
			publicResult, rootResult)
	}
}

func TestAssetCatalogRejectsInvalidBatchAndDigest(t *testing.T) {
	useMemoryPersistentCache(t)
	mapper := &assetMapper{}
	if _, err := mapper.Query(types.AssetQuery{AssetIds: []string{"bad id"}}, false); !errors.Is(err, types.ErrMalformed) {
		t.Fatalf("invalid exact lookup ID: got %v", err)
	}
	if _, err := mapper.Apply(types.AssetMutation{
		Op: "upsert_pack", Pack: &types.AssetPack{Id: "pack", Name: "Pack"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := mapper.Apply(types.AssetMutation{
		Op: "upsert_asset",
		Asset: &types.MediaAsset{
			Id: "asset", PackId: "pack", Kind: "gif",
			URL: "https://cdn.example.test/asset.gif", SHA256: "not-a-digest",
		},
	}); !errors.Is(err, types.ErrMalformed) {
		t.Fatalf("invalid digest: got %v", err)
	}
}
