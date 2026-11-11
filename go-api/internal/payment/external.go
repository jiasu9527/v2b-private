package payment

import "context"

// ExternalCheckoutManager can persist/serialize a business's gateway creation.
// It is called by the shared checkout entry point as well as business routes, so
// using the generic payment endpoint cannot bypass an order's idempotency guard.
type ExternalCheckoutManager interface {
	CheckoutExternalOnce(context.Context, int64, CheckoutRequest, func(context.Context) (CheckoutResult, error)) (CheckoutResult, error)
}

type externalOrderManagers []ExternalOrderManager

// NewExternalOrderManagers dispatches independent order namespaces without
// changing the existing subscription payment or Apple ID fulfillment paths.
func NewExternalOrderManagers(managers ...ExternalOrderManager) ExternalOrderManager {
	result := externalOrderManagers{}
	for _, manager := range managers {
		if manager != nil {
			result = append(result, manager)
		}
	}
	return result
}

func (m externalOrderManagers) OwnsExternalTradeNo(trade string) bool {
	for _, manager := range m {
		if manager.OwnsExternalTradeNo(trade) {
			return true
		}
	}
	return false
}

func (m externalOrderManagers) owner(trade string) (ExternalOrderManager, error) {
	var owner ExternalOrderManager
	for _, manager := range m {
		if manager.OwnsExternalTradeNo(trade) {
			if owner != nil {
				return nil, ErrInvalidParameter // Ambiguous namespaces must fail closed.
			}
			owner = manager
		}
	}
	if owner == nil {
		return nil, ErrOrderPaidOrMissing
	}
	return owner, nil
}

func (m externalOrderManagers) LookupExternalOrder(ctx context.Context, user int64, trade string) (ExternalOrder, error) {
	owner, err := m.owner(trade)
	if err != nil {
		return ExternalOrder{}, err
	}
	return owner.LookupExternalOrder(ctx, user, trade)
}

func (m externalOrderManagers) SetExternalPayment(ctx context.Context, user int64, trade string, method, fee int64) error {
	owner, err := m.owner(trade)
	if err != nil {
		return err
	}
	return owner.SetExternalPayment(ctx, user, trade, method, fee)
}

func (m externalOrderManagers) MarkExternalPaid(ctx context.Context, trade, callback string, manual bool, method, amount *int64) error {
	owner, err := m.owner(trade)
	if err != nil {
		return err
	}
	return owner.MarkExternalPaid(ctx, trade, callback, manual, method, amount)
}

func (m externalOrderManagers) CheckoutExternalOnce(ctx context.Context, user int64, req CheckoutRequest, create func(context.Context) (CheckoutResult, error)) (CheckoutResult, error) {
	owner, err := m.owner(req.TradeNo)
	if err != nil {
		return CheckoutResult{}, err
	}
	if guard, ok := owner.(ExternalCheckoutManager); ok {
		return guard.CheckoutExternalOnce(ctx, user, req, create)
	}
	return create(ctx)
}
