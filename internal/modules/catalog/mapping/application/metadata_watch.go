package application

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/logger"
	mappingdomain "github.com/dujiao-next/internal/modules/catalog/mapping/domain"
	notificationcontract "github.com/dujiao-next/internal/modules/notification/contract"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/upstream"
)

// Metadata snapshots contain hashes and short excerpts, never full product content.
// A nil upstream field is treated as omitted (not deleted), protecting against
// incomplete upstream list responses. An explicitly empty map clears the field.
const upstreamMetadataBizType = "upstream_product_metadata"

type upstreamMetadataChange struct {
	Field  string
	Locale string
	Before string
	After  string
}

func metadataPreview(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 180 {
		return string(runes[:180]) + "…"
	}
	return value
}

func metadataDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func metadataSnapshot(previous jsonmap.JSON, product *upstream.UpstreamProduct) (jsonmap.JSON, []upstreamMetadataChange) {
	next := make(jsonmap.JSON, len(previous))
	for k, v := range previous {
		next[k] = v
	}
	if product == nil {
		return next, nil
	}
	fields := []struct {
		key   string
		value jsonmap.JSON
	}{
		{"title", product.Title},
		{"description", product.Description},
		{"content", product.Content},
	}
	var changes []upstreamMetadataChange
	for _, field := range fields {
		if field.value == nil {
			// The upstream API may omit this field from its list response.
			continue
		}
		prefix := field.key + ":"
		hadBaseline := false
		locales := map[string]struct{}{}
		for key := range previous {
			if strings.HasPrefix(key, prefix) && strings.HasSuffix(key, ":hash") {
				locale := strings.TrimSuffix(strings.TrimPrefix(key, prefix), ":hash")
				locales[locale] = struct{}{}
				hadBaseline = true
			}
		}
		for locale := range field.value {
			locales[locale] = struct{}{}
		}
		ordered := make([]string, 0, len(locales))
		for locale := range locales {
			ordered = append(ordered, locale)
		}
		sort.Strings(ordered)
		for _, locale := range ordered {
			hashKey := prefix + locale + ":hash"
			previewKey := prefix + locale + ":preview"
			oldHash, oldExists := previous[hashKey].(string)
			oldPreview, _ := previous[previewKey].(string)
			rawValue, exists := field.value[locale]
			if !exists {
				if oldExists && hadBaseline {
					changes = append(changes, upstreamMetadataChange{Field: field.key, Locale: locale, Before: oldPreview, After: "（已删除）"})
				}
				delete(next, hashKey)
				delete(next, previewKey)
				continue
			}
			value, ok := rawValue.(string)
			if !ok {
				// Unexpected upstream data is not interpreted as a content update.
				continue
			}
			hash := metadataDigest(value)
			excerpt := metadataPreview(value)
			if hadBaseline && (!oldExists || oldHash != hash) {
				before := oldPreview
				if !oldExists {
					before = "（此前无此语言）"
				}
				changes = append(changes, upstreamMetadataChange{Field: field.key, Locale: locale, Before: before, After: excerpt})
			}
			next[hashKey] = hash
			next[previewKey] = excerpt
		}
	}
	return next, changes
}

func initialUpstreamMetadataSnapshot(product *upstream.UpstreamProduct) jsonmap.JSON {
	result, _ := metadataSnapshot(nil, product)
	return result
}

func preferredProductTitle(product *upstream.UpstreamProduct) string {
	if product == nil {
		return ""
	}
	for _, locale := range []string{"zh-CN", "en-US", "zh-TW"} {
		if title, ok := product.Title[locale].(string); ok && strings.TrimSpace(title) != "" {
			return metadataPreview(title)
		}
	}
	return ""
}

// observeUpstreamMetadata only detects and notifies: local product copy is NEVER overwritten.
// The snapshot advances only after the notification is successfully enqueued, so a
// temporary queue failure can be retried on the next sync without losing the alert.
func (s *Service) observeUpstreamMetadata(mapping *mappingdomain.Mapping, product *upstream.UpstreamProduct) {
	if s == nil || mapping == nil || product == nil {
		return
	}
	next, changes := metadataSnapshot(mapping.UpstreamMetadataJSON, product)
	if len(next) == 0 {
		return
	}
	if len(changes) == 0 {
		mapping.UpstreamMetadataJSON = next
		return
	}
	if s.notification == nil {
		logger.Warnw("upstream_metadata_alert_notifier_missing", "mapping_id", mapping.ID)
		return
	}
	labels := map[string]string{"title": "商品标题", "description": "简短描述", "content": "详细介绍"}
	details := []string{
		fmt.Sprintf("本地商品 ID：%d；映射 ID：%d；上游商品 ID：%d", mapping.LocalProductID, mapping.ID, mapping.UpstreamProductID),
	}
	for index, change := range changes {
		if index == 12 {
			details = append(details, fmt.Sprintf("另有 %d 项语言字段变更，请在后台核查。", len(changes)-index))
			break
		}
		details = append(details, fmt.Sprintf("%s [%s]\n原上游内容：%s\n新上游内容：%s", labels[change.Field], change.Locale, change.Before, change.After))
	}
	title := preferredProductTitle(product)
	if title == "" {
		title = fmt.Sprintf("商品 #%d", mapping.LocalProductID)
	}
	err := s.notification.Enqueue(notificationcontract.EnqueueInput{
		EventType: constants.NotificationEventExceptionAlert,
		BizType:   upstreamMetadataBizType,
		BizID:     mapping.LocalProductID,
		Data: jsonmap.JSON{
			"product_title":          title,
			"alert_type":             "上游商品信息变更",
			"alert_level":            "warning",
			"alert_value":            fmt.Sprintf("%d 项", len(changes)),
			"alert_threshold":        "人工核对",
			"message":                "上游商品文案已变化，本地标题和介绍未被自动覆盖。请核对销售期限、权益和交付内容；必要时先下架商品。",
			"affected_items_summary": strings.Join(details, "\n\n"),
		},
	})
	if err != nil {
		logger.Warnw("upstream_metadata_alert_enqueue_failed", "mapping_id", mapping.ID, "error", err)
		return
	}
	mapping.UpstreamMetadataJSON = next
}
