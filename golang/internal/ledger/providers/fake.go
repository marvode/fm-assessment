package providers

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// Record is what the fake provider remembers about a charge or payout.
type Record struct {
	IsPayout            bool
	ReferenceID         string
	ProviderReferenceID string
	Amount              int64
	Status              PayoutStatus
}

// FakePaymentProvider is an in-memory stand-in for a real provider. Tests script payout behaviour; the dev endpoint
// reads it to fake webhooks. Its memory is lost on restart, which the e2e script relies on.
type FakePaymentProvider struct {
	mu             sync.Mutex
	charges        map[string]*Record
	payouts        map[string]*Record
	payoutAttempts int
	behaviour      func(PayoutInstruction) (PayoutInitiation, error)
}

func NewFakePaymentProvider() *FakePaymentProvider {
	return &FakePaymentProvider{charges: map[string]*Record{}, payouts: map[string]*Record{}}
}

// SetPayoutBehaviour decides new payouts. Nil accepts them; returning an error simulates a transient provider fault.
func (p *FakePaymentProvider) SetPayoutBehaviour(behaviour func(PayoutInstruction) (PayoutInitiation, error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.behaviour = behaviour
}

func (p *FakePaymentProvider) PayoutAttempts() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.payoutAttempts
}

func (p *FakePaymentProvider) InitiateCharge(_ context.Context, referenceID string, amount int64) (ChargeInitiation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	record, ok := p.charges[referenceID]
	if !ok {
		record = &Record{ReferenceID: referenceID, ProviderReferenceID: "chg_" + compact(uuid.New()), Amount: amount, Status: StatusPending}
		p.charges[referenceID] = record
	}
	return ChargeInitiation{ProviderReferenceID: record.ProviderReferenceID}, nil
}

func (p *FakePaymentProvider) InitiatePayout(_ context.Context, instruction PayoutInstruction) (PayoutInitiation, error) {
	p.mu.Lock()
	p.payoutAttempts++
	if existing, ok := p.payouts[instruction.ReferenceID]; ok {
		defer p.mu.Unlock()
		if existing.Status == StatusFailed {
			return RejectedPayout("The payout already failed."), nil
		}
		return AcceptedPayout(existing.ProviderReferenceID), nil
	}
	behaviour := p.behaviour
	p.mu.Unlock()

	initiation := AcceptedPayout("trf_" + compact(uuid.New()))
	if behaviour != nil {
		var err error
		if initiation, err = behaviour(instruction); err != nil {
			return PayoutInitiation{}, err
		}
	}
	if initiation.Outcome == Accepted && initiation.ProviderReferenceID != nil {
		p.mu.Lock()
		p.payouts[instruction.ReferenceID] = &Record{
			IsPayout: true, ReferenceID: instruction.ReferenceID, ProviderReferenceID: *initiation.ProviderReferenceID,
			Amount: instruction.Amount, Status: StatusPending,
		}
		p.mu.Unlock()
	}
	return initiation, nil
}

func (p *FakePaymentProvider) GetPayoutStatus(_ context.Context, referenceID string) (PayoutStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if record, ok := p.payouts[referenceID]; ok {
		return record.Status, nil
	}
	return StatusNotFound, nil
}

func (p *FakePaymentProvider) SetPayoutStatus(referenceID string, status PayoutStatus) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if record, ok := p.payouts[referenceID]; ok {
		record.Status = status
	}
}

func (p *FakePaymentProvider) FindByProviderReference(providerReferenceID string) (Record, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, records := range []map[string]*Record{p.charges, p.payouts} {
		for _, record := range records {
			if record.ProviderReferenceID == providerReferenceID {
				return *record, true
			}
		}
	}
	return Record{}, false
}
