package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// shareRepositoryFake answers the share and network rule repository calls
// and counts them; validation failures must never reach it.
type shareRepositoryFake struct {
	AccountRepository
	calls      int
	redemption domain.ShareRedemption
}

func (f *shareRepositoryFake) ListNetworkRules(context.Context, domain.Actor) ([]domain.NetworkRule, error) {
	f.calls++
	return nil, nil
}
func (f *shareRepositoryFake) CreateNetworkRule(context.Context, domain.Actor, domain.NetworkRuleInput) (domain.NetworkRule, error) {
	f.calls++
	return domain.NetworkRule{}, nil
}
func (f *shareRepositoryFake) UpdateNetworkRule(context.Context, domain.Actor, string, domain.NetworkRuleInput) (domain.NetworkRule, error) {
	f.calls++
	return domain.NetworkRule{}, nil
}
func (f *shareRepositoryFake) DeleteNetworkRule(context.Context, domain.Actor, string) error {
	f.calls++
	return nil
}
func (f *shareRepositoryFake) ListShares(context.Context, domain.Actor) ([]domain.Share, error) {
	f.calls++
	return nil, nil
}
func (f *shareRepositoryFake) GetShare(context.Context, domain.Actor, string) (domain.Share, error) {
	f.calls++
	return domain.Share{}, nil
}
func (f *shareRepositoryFake) CreateShare(_ context.Context, _ domain.Actor, in domain.ShareInput) (domain.ShareGrant, error) {
	f.calls++
	if in.ExpiresAt.Location() != time.UTC {
		return domain.ShareGrant{}, errors.New("expiry not in UTC")
	}
	return domain.ShareGrant{}, nil
}
func (f *shareRepositoryFake) RevokeShare(context.Context, domain.Actor, string) (domain.Share, error) {
	f.calls++
	return domain.Share{}, nil
}
func (f *shareRepositoryFake) ListShareAccess(context.Context, domain.Actor, string, string, int) ([]domain.ShareAccessRecord, string, error) {
	f.calls++
	return nil, "", nil
}
func (f *shareRepositoryFake) RedeemShare(_ context.Context, in domain.ShareRedemption) (domain.SessionGrant, error) {
	f.calls++
	f.redemption = in
	return domain.SessionGrant{}, nil
}
func (f *shareRepositoryFake) CurrentShare(context.Context, domain.Actor) (domain.GuestShare, error) {
	f.calls++
	return domain.GuestShare{}, nil
}

func TestSharesValidateBeforeStorage(t *testing.T) {
	repo := &shareRepositoryFake{}
	a := accountService(t, repo, accountPasswordFake{})
	ctx, actor, bad := context.Background(), accountTestActor(), domain.Actor{}
	id := "11111111-1111-4111-8111-111111111111"
	rule := domain.NetworkRuleInput{LibraryID: id, Network: "lan"}
	share := domain.ShareInput{LibraryID: id, ExpiresAt: time.Now().In(time.FixedZone("x", 3600)).Add(time.Hour), MaxStreams: 1}
	refused := []error{}
	collect := func(err error) { refused = append(refused, err) }
	_, err := a.NetworkRules(ctx, bad)
	collect(err)
	_, err = a.CreateNetworkRule(ctx, bad, rule)
	collect(err)
	_, err = a.CreateNetworkRule(ctx, actor, domain.NetworkRuleInput{LibraryID: id, Network: "x"})
	collect(err)
	_, err = a.UpdateNetworkRule(ctx, actor, "x", rule)
	collect(err)
	_, err = a.UpdateNetworkRule(ctx, actor, id, domain.NetworkRuleInput{})
	collect(err)
	collect(a.DeleteNetworkRule(ctx, actor, "x"))
	_, err = a.Shares(ctx, bad)
	collect(err)
	_, err = a.Share(ctx, actor, "x")
	collect(err)
	_, err = a.CreateShare(ctx, bad, share)
	collect(err)
	_, err = a.CreateShare(ctx, actor, domain.ShareInput{LibraryID: id})
	collect(err)
	_, err = a.RevokeShare(ctx, actor, "x")
	collect(err)
	_, _, err = a.ShareAccess(ctx, actor, "x", "", 10)
	collect(err)
	_, _, err = a.ShareAccess(ctx, actor, id, "", 0)
	collect(err)
	_, err = a.RedeemShare(ctx, "t", "a\x00", "")
	collect(err)
	_, err = a.RedeemShareNative(ctx, "t", domain.NativeClient{}, "")
	collect(err)
	_, err = a.CurrentShare(ctx, bad)
	collect(err)
	for i, err := range refused {
		if err == nil {
			t.Errorf("call %d accepted", i)
		}
	}
	if repo.calls != 0 {
		t.Fatalf("invalid calls reached storage %d times", repo.calls)
	}
	ok := []error{}
	_, err = a.NetworkRules(ctx, actor)
	ok = append(ok, err)
	_, err = a.CreateNetworkRule(ctx, actor, rule)
	ok = append(ok, err)
	_, err = a.UpdateNetworkRule(ctx, actor, id, rule)
	ok = append(ok, err, a.DeleteNetworkRule(ctx, actor, id))
	_, err = a.Shares(ctx, actor)
	ok = append(ok, err)
	_, err = a.Share(ctx, actor, id)
	ok = append(ok, err)
	_, err = a.CreateShare(ctx, actor, share)
	ok = append(ok, err)
	_, err = a.RevokeShare(ctx, actor, id)
	ok = append(ok, err)
	_, _, err = a.ShareAccess(ctx, actor, id, "", 10)
	ok = append(ok, err)
	_, err = a.CurrentShare(ctx, actor)
	ok = append(ok, err)
	_, err = a.RedeemShare(ctx, "t", "browser", "192.0.2.1")
	ok = append(ok, err)
	if repo.redemption.Native || repo.redemption.MaxSessions != 7 || repo.redemption.SessionTTL != 12*time.Hour {
		t.Fatalf("web redemption %+v", repo.redemption)
	}
	_, err = a.RedeemShareNative(ctx, "t", domain.NativeClient{Name: "Player", DeviceID: "tv", Device: "TV"}, "192.0.2.1")
	ok = append(ok, err)
	if !repo.redemption.Native || repo.redemption.DeviceName != "TV" {
		t.Fatalf("native redemption %+v", repo.redemption)
	}
	for i, err := range ok {
		if err != nil {
			t.Errorf("call %d: %v", i, err)
		}
	}
}
