package retrieval

import (
	"fmt"
	"strings"
)

func exactMetadataFromPayload(payload map[string]interface{}, fields []string) map[string]string {
	if len(payload) == 0 || len(fields) == 0 {
		return nil
	}

	metadata := make(map[string]string)
	if raw, ok := payload["metadata"]; ok {
		copyExactMetadataValues(metadata, toStringInterfaceMap(raw), fields)
	}
	copyExactMetadataValues(metadata, payload, fields)
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

func copyExactMetadataValues(dst map[string]string, src map[string]interface{}, fields []string) {
	if len(src) == 0 {
		return
	}
	for _, field := range fields {
		key := strings.TrimSpace(field)
		if key == "" {
			continue
		}
		if value, ok := metadataStringValue(src[key]); ok {
			dst[key] = value
		}
	}
}

func toStringInterfaceMap(raw interface{}) map[string]interface{} {
	switch v := raw.(type) {
	case map[string]interface{}:
		return v
	case map[string]string:
		out := make(map[string]interface{}, len(v))
		for key, value := range v {
			out[key] = value
		}
		return out
	default:
		return nil
	}
}

func metadataStringValue(raw interface{}) (string, bool) {
	switch v := raw.(type) {
	case string:
		v = strings.TrimSpace(v)
		return v, v != ""
	case fmt.Stringer:
		value := strings.TrimSpace(v.String())
		return value, value != ""
	case float64, float32, int, int64, int32, uint, uint64, uint32:
		return strings.TrimSpace(fmt.Sprint(v)), true
	default:
		return "", false
	}
}

func mergeCandidateMetadata(dst, src map[string]string) map[string]string {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]string, len(src))
	}
	for key, value := range src {
		if _, exists := dst[key]; !exists {
			dst[key] = value
		}
	}
	return dst
}
