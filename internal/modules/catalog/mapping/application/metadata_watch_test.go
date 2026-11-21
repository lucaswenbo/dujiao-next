package application

import (
	"errors"
	"strings"
	"testing"

	mappingdomain "github.com/dujiao-next/internal/modules/catalog/mapping/domain"
	notificationcontract "github.com/dujiao-next/internal/modules/notification/contract"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/upstream"
)

type recordingMetadataNotifier struct {
	events []notificationcontract.EnqueueInput
	err    error
}

func (n *recordingMetadataNotifier) Enqueue(input notificationcontract.EnqueueInput) error {
	if n.err != nil {
		return n.err
	}
	n.events = append(n.events, input)
	return nil
}

func TestUpstreamMetadataBaselineDoesNotAlert(t *testing.T) {
	product := &upstream.UpstreamProduct{
		Title:       jsonmap.JSON{"zh-CN": "Gemini 6个月"},
		Description: jsonmap.JSON{"zh-CN": "6个月完整权益"},
		Content:     jsonmap.JSON{"zh-CN": "兑换码"},
	}
	snapshot, changes := metadataSnapshot(nil, product)
	if len(changes) != 0 || len(snapshot) != 6 {
		t.Fatalf("initial scan must create baseline without changes: %v, %+v", snapshot, changes)
	}
	_, repeated := metadataSnapshot(snapshot, product)
	if len(repeated) != 0 {
		t.Fatalf("unchanged metadata must not alert: %+v", repeated)
	}
}

func TestUpstreamMetadataChangedTitleEnqueuesOnce(t *testing.T) {
	oldProduct := &upstream.UpstreamProduct{Title: jsonmap.JSON{"zh-CN": "Gemini 6个月"}}
	nextProduct := &upstream.UpstreamProduct{Title: jsonmap.JSON{"zh-CN": "Gemini 3个月"}}
	mapping := &mappingdomain.Mapping{
		ID: 11, LocalProductID: 22, UpstreamProductID: 33,
		UpstreamMetadataJSON: initialUpstreamMetadataSnapshot(oldProduct),
	}
	notifier := &recordingMetadataNotifier{}
	service := &Service{notification: notifier}
	service.observeUpstreamMetadata(mapping, nextProduct)
	service.observeUpstreamMetadata(mapping, nextProduct)
	if len(notifier.events) != 1 {
		t.Fatalf("expected one notification, got %d", len(notifier.events))
	}
	event := notifier.events[0]
	if event.BizID != 22 || event.BizType != upstreamMetadataBizType {
		t.Fatalf("unexpected notification metadata: %+v", event)
	}
	summary, _ := event.Data["affected_items_summary"].(string)
	if !strings.Contains(summary, "6个月") || !strings.Contains(summary, "3个月") {
		t.Fatalf("expected before/after detail, got %s", summary)
	}
}

func TestUpstreamMetadataFailedEnqueueRetainsOldSnapshot(t *testing.T) {
	before := &upstream.UpstreamProduct{Title: jsonmap.JSON{"zh-CN": "6个月"}}
	after := &upstream.UpstreamProduct{Title: jsonmap.JSON{"zh-CN": "3个月"}}
	mapping := &mappingdomain.Mapping{
		ID: 1, LocalProductID: 2, UpstreamMetadataJSON: initialUpstreamMetadataSnapshot(before),
	}
	notifier := &recordingMetadataNotifier{err: errors.New("queue unavailable")}
	service := &Service{notification: notifier}
	service.observeUpstreamMetadata(mapping, after)
	if mapping.UpstreamMetadataJSON["title:zh-CN:hash"] != metadataDigest("6个月") {
		t.Fatal("failed notification must not advance metadata snapshot")
	}
	notifier.err = nil
	service.observeUpstreamMetadata(mapping, after)
	if len(notifier.events) != 1 || mapping.UpstreamMetadataJSON["title:zh-CN:hash"] != metadataDigest("3个月") {
		t.Fatal("next sync must retry alert and then save new baseline")
	}
}

func TestUpstreamMetadataNilFieldsDoNotTriggerFalseRemoval(t *testing.T) {
	before := &upstream.UpstreamProduct{Content: jsonmap.JSON{"zh-CN": "说明内容"}}
	snapshot := initialUpstreamMetadataSnapshot(before)
	next, changes := metadataSnapshot(snapshot, &upstream.UpstreamProduct{})
	if len(changes) != 0 || next["content:zh-CN:hash"] != snapshot["content:zh-CN:hash"] {
		t.Fatalf("omitted metadata should preserve baseline: %v, %+v", next, changes)
	}
	_, cleared := metadataSnapshot(snapshot, &upstream.UpstreamProduct{Content: jsonmap.JSON{}})
	if len(cleared) != 1 || cleared[0].After != "（已删除）" {
		t.Fatalf("explicitly empty content should be detected: %+v", cleared)
	}
}

func TestUpstreamMetadataTracksIndividualLocales(t *testing.T) {
	oldProduct := &upstream.UpstreamProduct{
		Title: jsonmap.JSON{"zh-CN": "六个月", "en-US": "Six months"},
	}
	nextProduct := &upstream.UpstreamProduct{
		Title: jsonmap.JSON{"zh-CN": "六个月", "en-US": "Three months"},
	}
	_, changes := metadataSnapshot(initialUpstreamMetadataSnapshot(oldProduct), nextProduct)
	if len(changes) != 1 || changes[0].Locale != "en-US" || changes[0].Field != "title" {
		t.Fatalf("unexpected changes: %+v", changes)
	}
}
