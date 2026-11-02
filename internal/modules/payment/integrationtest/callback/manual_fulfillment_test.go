package paymentcallback_test

import (
	"errors"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	productgormstore "github.com/dujiao-next/internal/modules/catalog/product/store/gormstore"
	ordergormstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"
	paymentapp "github.com/dujiao-next/internal/modules/payment/application"
	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"
	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"
	paymentgormstore "github.com/dujiao-next/internal/modules/payment/infrastructure/gormstore"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/shopspring/decimal"
)

type manualFulfillmentQueue struct {
	paymentcontract.Queue
	calls          int
	fail           bool
	enabled        bool
	duringDispatch func()
}

func (q *manualFulfillmentQueue) Enabled() bool                         { return q.enabled }
func (q *manualFulfillmentQueue) EnqueueStatusEmail(uint, string) error { return nil }
func (q *manualFulfillmentQueue) EnqueueOrderAutoFulfill(uint) error {
	q.calls++
	if q.duringDispatch != nil {
		q.duringDispatch()
	}
	if q.fail {
		return errors.New("queue unavailable")
	}
	return nil
}

type manualFulfillmentMember struct{ calls int }

func (m *manualFulfillmentMember) OnRechargeCompleted(uint, decimal.Decimal) error { return nil }
func (m *manualFulfillmentMember) OnOrderPaid(uint, decimal.Decimal) error         { m.calls++; return nil }

type manualFulfillmentProcurement struct{ calls int }

func (p *manualFulfillmentProcurement) CreateForOrder(uint) error { p.calls++; return nil }

func setupManualHold(t *testing.T, status string) (*manualConfirmFixture, *manualFulfillmentQueue, *manualFulfillmentMember, *manualFulfillmentProcurement) {
	t.Helper()
	f := newManualConfirmFixture(t, status, 1)
	db, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	f.seedAvailableSecrets(t, 1)
	if err := f.db.Model(f.order).Update("user_id", 7).Error; err != nil {
		t.Fatal(err)
	}
	q := &manualFulfillmentQueue{enabled: true}
	secret := "test-guest-credential-secret-with-32-bytes"
	f.service = paymentapp.NewPaymentService(paymentapp.PaymentServiceOptions{
		OrderStore: ordergormstore.New(f.db, secret), ProductRepo: productgormstore.NewProductStore(f.db), ProductSKURepo: productgormstore.NewSKUStore(f.db),
		PaymentStore: paymentgormstore.New(f.db, secret), ChannelStore: paymentgormstore.NewChannelStore(f.db), Queue: q, ExpireMinutes: 15,
	})
	m := &manualFulfillmentMember{}
	p := &manualFulfillmentProcurement{}
	f.service.SetMemberLevelService(m)
	f.service.SetProcurementService(p)
	return f, q, m, p
}
func holdInput(f *manualConfirmFixture, hold bool) paymentapp.ManualConfirmPaymentInput {
	return paymentapp.ManualConfirmPaymentInput{OrderID: f.order.ID, PaymentID: f.payment.ID, AdminID: 7, ProviderRef: "TEST-RECEIPT", Remark: "verified", HoldFulfillment: hold}
}
func storedHold(t *testing.T, f *manualConfirmFixture) bool {
	t.Helper()
	var p paymentdomain.Payment
	if err := f.db.First(&p, f.payment.ID).Error; err != nil {
		t.Fatal(err)
	}
	return p.ProviderPayload["manual_fulfillment_hold"] == true
}
func TestManualPaymentHoldAndReleaseDoesNotRepeatSettlement(t *testing.T) {
	for _, status := range []string{constants.OrderStatusPendingPayment, constants.OrderStatusCanceled} {
		t.Run(status, func(t *testing.T) {
			f, q, m, p := setupManualHold(t, status)
			if _, err := f.service.ManualConfirmPayment(holdInput(f, true)); err != nil {
				t.Fatal(err)
			}
			paid := f.reloadOrder(t)
			if paid.PaidAt == nil || !storedHold(t, f) || q.calls != 0 || p.calls != 0 || m.calls != 1 {
				t.Fatalf("hold did not settle without dispatch: queue=%d procurement=%d membership=%d", q.calls, p.calls, m.calls)
			}
			if _, err := f.service.ManualConfirmPayment(holdInput(f, true)); err != nil {
				t.Fatal(err)
			}
			// A late provider callback cannot clear the hold, including a conflicting payload key.
			if _, err := f.service.HandleCallback(paymentapp.PaymentCallbackInput{PaymentID: f.payment.ID, Status: constants.PaymentStatusSuccess, Amount: f.payment.Amount, Currency: f.payment.Currency, Payload: jsonmap.JSON{"manual_fulfillment_hold": false}}); err != nil {
				t.Fatal(err)
			}
			if !storedHold(t, f) || q.calls != 0 || p.calls != 0 {
				t.Fatal("late callback dispatched or cleared hold")
			}
			for i := 0; i < 2; i++ {
				if _, err := f.service.ManualConfirmPayment(holdInput(f, false)); err != nil {
					t.Fatal(err)
				}
			}
			after := f.reloadOrder(t)
			if storedHold(t, f) || q.calls != 1 || p.calls != 1 || m.calls != 1 || !paid.PaidAt.Equal(*after.PaidAt) {
				t.Fatalf("release repeated settlement/dispatch: queue=%d procurement=%d membership=%d", q.calls, p.calls, m.calls)
			}
		})
	}
}
func TestManualPaymentReleaseFailurePreservesHoldAndCanRetry(t *testing.T) {
	f, q, _, _ := setupManualHold(t, constants.OrderStatusPendingPayment)
	if _, err := f.service.ManualConfirmPayment(holdInput(f, true)); err != nil {
		t.Fatal(err)
	}
	q.fail = true
	if _, err := f.service.ManualConfirmPayment(holdInput(f, false)); err == nil {
		t.Fatal("expected queue failure")
	}
	if !storedHold(t, f) {
		t.Fatal("failed release lost hold")
	}
	q.fail = false
	if _, err := f.service.ManualConfirmPayment(holdInput(f, false)); err != nil {
		t.Fatal(err)
	}
	if storedHold(t, f) {
		t.Fatal("successful retry retained hold")
	}
}
func TestManualPaymentReleaseLeaseFencesConcurrentRequests(t *testing.T) {
	f, q, _, _ := setupManualHold(t, constants.OrderStatusPendingPayment)
	if _, err := f.service.ManualConfirmPayment(holdInput(f, true)); err != nil {
		t.Fatal(err)
	}
	q.duringDispatch = func() {
		if _, err := f.service.ManualConfirmPayment(holdInput(f, false)); err == nil {
			t.Error("concurrent release acquired active lease")
		}
	}
	if _, err := f.service.ManualConfirmPayment(holdInput(f, false)); err != nil {
		t.Fatal(err)
	}
	if q.calls != 1 {
		t.Fatal("duplicate dispatch", q.calls)
	}
}
func TestManualPaymentReleaseRecoversExpiredLease(t *testing.T) {
	f, _, _, _ := setupManualHold(t, constants.OrderStatusPendingPayment)
	if _, err := f.service.ManualConfirmPayment(holdInput(f, true)); err != nil {
		t.Fatal(err)
	}
	var p paymentdomain.Payment
	if err := f.db.First(&p, f.payment.ID).Error; err != nil {
		t.Fatal(err)
	}
	p.ProviderPayload["manual_fulfillment_dispatch_until"] = time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
	if err := f.db.Model(&p).Update("provider_payload", p.ProviderPayload).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ManualConfirmPayment(holdInput(f, false)); err != nil {
		t.Fatal(err)
	}
}
func TestManualPaymentReleaseRejectsRefundedOrder(t *testing.T) {
	f, q, _, _ := setupManualHold(t, constants.OrderStatusPendingPayment)
	if _, err := f.service.ManualConfirmPayment(holdInput(f, true)); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(f.order).Update("status", constants.OrderStatusRefunded).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ManualConfirmPayment(holdInput(f, false)); err == nil {
		t.Fatal("refunded order released")
	}
	if !storedHold(t, f) || q.calls != 0 {
		t.Fatal("refunded order dispatched")
	}
}
