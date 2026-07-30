package store

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"

	"chat/server/store/types"
)

const (
	compressedCachePrefix = "gz1:"
	// MySQL 的 kvmeta.value 使用 TEXT；预留字符集与驱动开销，不写满 64 KiB。
	maxPersistentCacheValue = 60 * 1024
)

func marshalPersistentJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var compressed bytes.Buffer
	compressed.WriteString(compressedCachePrefix)
	encoder := base64.NewEncoder(base64.RawStdEncoding, &compressed)
	writer := gzip.NewWriter(encoder)
	if _, err = writer.Write(raw); err != nil {
		return "", err
	}
	if err = writer.Close(); err != nil {
		return "", err
	}
	if err = encoder.Close(); err != nil {
		return "", err
	}
	if compressed.Len() > maxPersistentCacheValue {
		return "", types.ErrPolicy
	}
	return compressed.String(), nil
}

func unmarshalPersistentJSON(encoded string, target any) error {
	if len(encoded) < len(compressedCachePrefix) ||
		encoded[:len(compressedCachePrefix)] != compressedCachePrefix {
		return json.Unmarshal([]byte(encoded), target)
	}
	decoder := base64.NewDecoder(base64.RawStdEncoding,
		bytes.NewBufferString(encoded[len(compressedCachePrefix):]))
	reader, err := gzip.NewReader(decoder)
	if err != nil {
		return err
	}
	defer reader.Close()
	raw, err := io.ReadAll(io.LimitReader(reader, 16*maxPersistentCacheValue+1))
	if err != nil {
		return err
	}
	if len(raw) > 16*maxPersistentCacheValue {
		return errors.New("persistent cache value expands beyond safety limit")
	}
	return json.Unmarshal(raw, target)
}
