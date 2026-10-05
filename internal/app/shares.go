package app

import (
	"context"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Share links, guest sessions (G48.6) and library network rules (G48.5).
// Administration is administrator-only and audited by the repository; the
// unified storage filter applies shares and rules on the next request.

// NetworkRules lists every library network rule.
func (a *Accounts) NetworkRules(ctx context.Context, actor domain.Actor) ([]domain.NetworkRule, error) {
	if !validActor(actor) {
		return nil, domain.ErrUnauthenticated
	}
	return a.repository.ListNetworkRules(ctx, actor)
}

// CreateNetworkRule adds a library network rule.
func (a *Accounts) CreateNetworkRule(ctx context.Context, actor domain.Actor, in domain.NetworkRuleInput) (domain.NetworkRule, error) {
	if !validActor(actor) {
		return domain.NetworkRule{}, domain.ErrUnauthenticated
	}
	if !in.Valid() {
		return domain.NetworkRule{}, domain.ErrInvalid
	}
	return a.repository.CreateNetworkRule(ctx, actor, in)
}

// UpdateNetworkRule replaces a library network rule.
func (a *Accounts) UpdateNetworkRule(ctx context.Context, actor domain.Actor, id string, in domain.NetworkRuleInput) (domain.NetworkRule, error) {
	if !validTarget(actor, id) {
		return domain.NetworkRule{}, domain.ErrNotFound
	}
	if !in.Valid() {
		return domain.NetworkRule{}, domain.ErrInvalid
	}
	return a.repository.UpdateNetworkRule(ctx, actor, id, in)
}

// DeleteNetworkRule removes a library network rule.
func (a *Accounts) DeleteNetworkRule(ctx context.Context, actor domain.Actor, id string) error {
	if !validTarget(actor, id) {
		return domain.ErrNotFound
	}
	return a.repository.DeleteNetworkRule(ctx, actor, id)
}

// Shares lists the newest share links.
func (a *Accounts) Shares(ctx context.Context, actor domain.Actor) ([]domain.Share, error) {
	if !validActor(actor) {
		return nil, domain.ErrUnauthenticated
	}
	return a.repository.ListShares(ctx, actor)
}

// Share reads one share link.
func (a *Accounts) Share(ctx context.Context, actor domain.Actor, id string) (domain.Share, error) {
	if !validTarget(actor, id) {
		return domain.Share{}, domain.ErrNotFound
	}
	return a.repository.GetShare(ctx, actor, id)
}

// CreateShare creates a share link; its token is returned only here.
func (a *Accounts) CreateShare(ctx context.Context, actor domain.Actor, in domain.ShareInput) (domain.ShareGrant, error) {
	if !validActor(actor) {
		return domain.ShareGrant{}, domain.ErrUnauthenticated
	}
	if !in.Valid(time.Now()) {
		return domain.ShareGrant{}, domain.ErrInvalid
	}
	in.ExpiresAt = in.ExpiresAt.UTC()
	return a.repository.CreateShare(ctx, actor, in)
}

// RevokeShare revokes a share link and every guest session it issued.
func (a *Accounts) RevokeShare(ctx context.Context, actor domain.Actor, id string) (domain.Share, error) {
	if !validTarget(actor, id) {
		return domain.Share{}, domain.ErrNotFound
	}
	return a.repository.RevokeShare(ctx, actor, id)
}

// ShareAccess pages the audited events of a share link.
func (a *Accounts) ShareAccess(ctx context.Context, actor domain.Actor, id, cursor string, limit int) ([]domain.ShareAccessRecord, string, error) {
	if !validTarget(actor, id) {
		return nil, "", domain.ErrNotFound
	}
	if limit < 1 || limit > domain.ShareAccessPageMax {
		return nil, "", domain.ErrInvalid
	}
	return a.repository.ListShareAccess(ctx, actor, id, cursor, limit)
}

// RedeemShare issues a web guest session for a share token. Like a web
// login it can browse but never play.
func (a *Accounts) RedeemShare(ctx context.Context, token, deviceName, ip string) (domain.SessionGrant, error) {
	if !validText(deviceName, 128, true) || len(ip) > 45 {
		return domain.SessionGrant{}, domain.ErrInvalid
	}
	return a.repository.RedeemShare(ctx, domain.ShareRedemption{Token: token, DeviceName: deviceName, IP: ip, MaxSessions: a.options.MaxSessions, SessionTTL: a.options.SessionTTL})
}

// RedeemShareNative issues a native guest session, which may direct play
// under the delivery rules, for a share that allows playback.
func (a *Accounts) RedeemShareNative(ctx context.Context, token string, client domain.NativeClient, ip string) (domain.SessionGrant, error) {
	if !ValidNativeClient(client) || len(ip) > 45 {
		return domain.SessionGrant{}, domain.ErrInvalid
	}
	return a.repository.RedeemShare(ctx, domain.ShareRedemption{Token: token, Native: true, Client: client, DeviceName: client.Device, IP: ip,
		MaxSessions: a.options.MaxSessions, SessionTTL: a.options.SessionTTL})
}

// CurrentShare describes the share of a guest session.
func (a *Accounts) CurrentShare(ctx context.Context, actor domain.Actor) (domain.GuestShare, error) {
	if !validActor(actor) {
		return domain.GuestShare{}, domain.ErrUnauthenticated
	}
	return a.repository.CurrentShare(ctx, actor)
}
