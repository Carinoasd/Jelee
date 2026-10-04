package app

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ClientControlRepository stores client control rules, policy, known clients
// and hit records (G47). Every method takes an administrator actor, checks it
// in its own transaction and audits each change.
type ClientControlRepository interface {
	ClientPolicy(context.Context, domain.Actor) (domain.ClientPolicy, error)
	SetClientPolicy(context.Context, domain.Actor, domain.ClientPolicy) (domain.ClientPolicy, error)
	ListClientRules(context.Context, domain.Actor) ([]domain.ClientRule, error)
	GetClientRule(context.Context, domain.Actor, string) (domain.ClientRule, error)
	CreateClientRule(context.Context, domain.Actor, domain.ClientRuleInput) (domain.ClientRule, error)
	UpdateClientRule(context.Context, domain.Actor, string, domain.ClientRuleInput) (domain.ClientRule, error)
	DeleteClientRule(context.Context, domain.Actor, string) error
	SetClientRuleEnforcing(context.Context, domain.Actor, string, bool) (domain.ClientRule, error)
	ListClientHits(context.Context, domain.Actor, domain.ClientHitFilter, string, int) ([]domain.ClientHitRecord, string, error)
	ExportClientHits(context.Context, domain.Actor, domain.ClientHitFilter, int) ([]domain.ClientHitRecord, error)
	ClientHitStats(context.Context, domain.Actor, time.Time, int) (domain.ClientHitStats, error)
	ListKnownClients(context.Context, domain.Actor, string, int) ([]domain.KnownClient, string, error)
	UpdateKnownClient(context.Context, domain.Actor, string, domain.KnownClientUpdate) (domain.KnownClient, error)
	BlockKnownClient(context.Context, domain.Actor, string) (domain.ClientRule, error)
	KickKnownClient(context.Context, domain.Actor, string) (int64, error)
}

// ClientRuleValidator compiles one rule with the request engine and reports
// why it cannot be enforced. It is supplied by the adapter that owns the
// engine, so an invalid pattern, regex or window never reaches storage.
type ClientRuleValidator func(domain.ClientRuleInput) error

// ClientControl is the administrator service for client control (G47).
// Changes take effect on the next request: storage bumps the policy
// version and every server instance recompiles when it sees a new one.
type ClientControl struct {
	repository ClientControlRepository
	validate   ClientRuleValidator
}

// Limits of the listing and statistics operations.
const (
	ClientListLimitMax = 100
	ClientStatsTopMax  = 50
	// ClientStatsWindowMax bounds how far back statistics reach.
	ClientStatsWindowMax = 30 * 24 * time.Hour
)

func NewClientControl(repository ClientControlRepository, validate ClientRuleValidator) (*ClientControl, error) {
	if repository == nil || validate == nil {
		return nil, errors.New("client control repository and rule validator must be provided")
	}
	return &ClientControl{repository: repository, validate: validate}, nil
}

func (c *ClientControl) Policy(ctx context.Context, actor domain.Actor) (domain.ClientPolicy, error) {
	if !validActor(actor) {
		return domain.ClientPolicy{}, domain.ErrUnauthenticated
	}
	return c.repository.ClientPolicy(ctx, actor)
}

func (c *ClientControl) SetPolicy(ctx context.Context, actor domain.Actor, policy domain.ClientPolicy) (domain.ClientPolicy, error) {
	if !validActor(actor) {
		return domain.ClientPolicy{}, domain.ErrUnauthenticated
	}
	if !policy.Valid() {
		return domain.ClientPolicy{}, domain.ErrInvalid
	}
	return c.repository.SetClientPolicy(ctx, actor, policy)
}

func (c *ClientControl) Rules(ctx context.Context, actor domain.Actor) ([]domain.ClientRule, error) {
	if !validActor(actor) {
		return nil, domain.ErrUnauthenticated
	}
	return c.repository.ListClientRules(ctx, actor)
}

func (c *ClientControl) Rule(ctx context.Context, actor domain.Actor, id string) (domain.ClientRule, error) {
	if !validTarget(actor, id) {
		return domain.ClientRule{}, domain.ErrNotFound
	}
	return c.repository.GetClientRule(ctx, actor, id)
}

func (c *ClientControl) checkRule(in domain.ClientRuleInput) (domain.ClientRuleInput, error) {
	in = in.Normalize()
	if !in.Valid() {
		return in, domain.ErrInvalid
	}
	if err := c.validate(in); err != nil {
		return in, domain.ErrInvalid
	}
	return in, nil
}

func (c *ClientControl) CreateRule(ctx context.Context, actor domain.Actor, in domain.ClientRuleInput) (domain.ClientRule, error) {
	if !validActor(actor) {
		return domain.ClientRule{}, domain.ErrUnauthenticated
	}
	in, err := c.checkRule(in)
	if err != nil {
		return domain.ClientRule{}, err
	}
	return c.repository.CreateClientRule(ctx, actor, in)
}

func (c *ClientControl) UpdateRule(ctx context.Context, actor domain.Actor, id string, in domain.ClientRuleInput) (domain.ClientRule, error) {
	if !validTarget(actor, id) {
		return domain.ClientRule{}, domain.ErrNotFound
	}
	in, err := c.checkRule(in)
	if err != nil {
		return domain.ClientRule{}, err
	}
	return c.repository.UpdateClientRule(ctx, actor, id, in)
}

func (c *ClientControl) DeleteRule(ctx context.Context, actor domain.Actor, id string) error {
	if !validTarget(actor, id) {
		return domain.ErrNotFound
	}
	return c.repository.DeleteClientRule(ctx, actor, id)
}

// SetRuleEnforcing switches an observe or shadow rule to the action it
// records (enforce), or an enforcing rule back to observing it (G47.3).
func (c *ClientControl) SetRuleEnforcing(ctx context.Context, actor domain.Actor, id string, enforce bool) (domain.ClientRule, error) {
	if !validTarget(actor, id) {
		return domain.ClientRule{}, domain.ErrNotFound
	}
	return c.repository.SetClientRuleEnforcing(ctx, actor, id, enforce)
}

func validClientPage(cursor string, limit int) bool {
	return limit >= 1 && limit <= ClientListLimitMax && len(cursor) <= 64
}

func (c *ClientControl) Hits(ctx context.Context, actor domain.Actor, filter domain.ClientHitFilter, cursor string, limit int) ([]domain.ClientHitRecord, string, error) {
	if !validActor(actor) {
		return nil, "", domain.ErrUnauthenticated
	}
	if !filter.Valid() || !validClientPage(cursor, limit) {
		return nil, "", domain.ErrInvalid
	}
	return c.repository.ListClientHits(ctx, actor, filter, cursor, limit)
}

func (c *ClientControl) ExportHits(ctx context.Context, actor domain.Actor, filter domain.ClientHitFilter) ([]domain.ClientHitRecord, error) {
	if !validActor(actor) {
		return nil, domain.ErrUnauthenticated
	}
	if !filter.Valid() {
		return nil, domain.ErrInvalid
	}
	return c.repository.ExportClientHits(ctx, actor, filter, domain.ClientHitExportMax)
}

func (c *ClientControl) Stats(ctx context.Context, actor domain.Actor, window time.Duration, top int, now time.Time) (domain.ClientHitStats, error) {
	if !validActor(actor) {
		return domain.ClientHitStats{}, domain.ErrUnauthenticated
	}
	if window <= 0 || window > ClientStatsWindowMax || top < 1 || top > ClientStatsTopMax {
		return domain.ClientHitStats{}, domain.ErrInvalid
	}
	return c.repository.ClientHitStats(ctx, actor, now.Add(-window), top)
}

func (c *ClientControl) Clients(ctx context.Context, actor domain.Actor, cursor string, limit int) ([]domain.KnownClient, string, error) {
	if !validActor(actor) {
		return nil, "", domain.ErrUnauthenticated
	}
	if !validClientPage(cursor, limit) {
		return nil, "", domain.ErrInvalid
	}
	return c.repository.ListKnownClients(ctx, actor, cursor, limit)
}

func (c *ClientControl) UpdateClient(ctx context.Context, actor domain.Actor, id string, update domain.KnownClientUpdate) (domain.KnownClient, error) {
	if !validTarget(actor, id) {
		return domain.KnownClient{}, domain.ErrNotFound
	}
	if !update.Valid() || update.Alias == nil && update.Trusted == nil {
		return domain.KnownClient{}, domain.ErrInvalid
	}
	return c.repository.UpdateKnownClient(ctx, actor, id, update)
}

// BlockClient adds a deny rule for the client's device ID, or for its user
// agent when it reports none.
func (c *ClientControl) BlockClient(ctx context.Context, actor domain.Actor, id string) (domain.ClientRule, error) {
	if !validTarget(actor, id) {
		return domain.ClientRule{}, domain.ErrNotFound
	}
	return c.repository.BlockKnownClient(ctx, actor, id)
}

// KickClient revokes every active session the client used.
func (c *ClientControl) KickClient(ctx context.Context, actor domain.Actor, id string) (int64, error) {
	if !validTarget(actor, id) {
		return 0, domain.ErrNotFound
	}
	return c.repository.KickKnownClient(ctx, actor, id)
}
