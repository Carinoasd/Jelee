package postgres

import (
	"context"
	"net/netip"
	"slices"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Network rules of libraries (G48.5). The unified filter (visibility.go)
// is their only reader; this file administers them. Every change is
// audited and takes effect on the next request: the filter evaluates the
// rules in SQL against the request parameter, without a cache.

const networkRuleColumns = `r.id::text,r.library_id::text,l.name,r.network,r.cidrs::text[],r.client_kinds,r.include_admins,r.enabled,r.note,r.created_at,r.updated_at`

const networkRuleFrom = ` FROM library_network_rules r JOIN libraries l ON l.id=r.library_id`

func scanNetworkRule(row pgx.Row) (domain.NetworkRule, error) {
	var v domain.NetworkRule
	err := row.Scan(&v.ID, &v.LibraryID, &v.LibraryName, &v.Network, &v.CIDRs, &v.ClientKinds, &v.IncludeAdmins, &v.Enabled, &v.Note, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return v, storageError(err)
	}
	if v.CIDRs == nil {
		v.CIDRs = []string{}
	}
	if v.ClientKinds == nil {
		v.ClientKinds = []string{}
	}
	v.CreatedAt, v.UpdatedAt = v.CreatedAt.UTC(), v.UpdatedAt.UTC()
	return v, nil
}

// normalizeNetworkRule parses every address or prefix into its masked
// prefix form (an address is a single-address prefix) and orders the
// lists, so equal rules compare equal.
func normalizeNetworkRule(in domain.NetworkRuleInput) (domain.NetworkRuleInput, bool) {
	if !in.Valid() {
		return in, false
	}
	cidrs := make([]string, 0, len(in.CIDRs))
	for _, c := range in.CIDRs {
		prefix, err := netip.ParsePrefix(c)
		if err != nil {
			addr, aerr := netip.ParseAddr(c)
			if aerr != nil || addr.Zone() != "" {
				return in, false
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		addr, bits := prefix.Addr(), prefix.Bits()
		if addr.Zone() != "" {
			return in, false
		}
		if addr.Is4In6() {
			// ::ffff:a.b.c.d/n is the IPv4 prefix a.b.c.d/(n-96).
			if bits < 96 {
				return in, false
			}
			addr, bits = addr.Unmap(), bits-96
		}
		prefix = netip.PrefixFrom(addr, bits).Masked()
		if !prefix.IsValid() {
			return in, false
		}
		cidrs = append(cidrs, prefix.String())
	}
	slices.Sort(cidrs)
	in.CIDRs = slices.Compact(cidrs)
	kinds := append([]string{}, in.ClientKinds...)
	slices.Sort(kinds)
	in.ClientKinds = kinds
	return in, true
}

func networkRuleAudit(v domain.NetworkRuleInput) map[string]any {
	return map[string]any{"libraryId": v.LibraryID, "network": v.Network, "cidrs": v.CIDRs, "clientKinds": v.ClientKinds, "includeAdmins": v.IncludeAdmins, "enabled": v.Enabled, "note": v.Note}
}

// ListNetworkRules lists every rule by library name, then creation.
func (s *Store) ListNetworkRules(ctx context.Context, actor domain.Actor) ([]domain.NetworkRule, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT `+networkRuleColumns+networkRuleFrom+` ORDER BY lower(l.name) COLLATE "C",l.id,r.created_at,r.id LIMIT $1`, domain.NetworkRulesMax)
	if err != nil {
		return nil, storageError(err)
	}
	rules := make([]domain.NetworkRule, 0)
	for rows.Next() {
		v, e := scanNetworkRule(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		rules = append(rules, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, storageError(err)
	}
	return rules, storageError(tx.Commit(ctx))
}

// CreateNetworkRule stores a rule; at most NetworkRulesMax exist.
func (s *Store) CreateNetworkRule(ctx context.Context, actor domain.Actor, in domain.NetworkRuleInput) (domain.NetworkRule, error) {
	in, ok := normalizeNetworkRule(in)
	if !ok {
		return domain.NetworkRule{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.NetworkRule{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('jelee.library_network_rules'))`); err != nil {
		return domain.NetworkRule{}, storageError(err)
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM library_network_rules`).Scan(&count); err != nil {
		return domain.NetworkRule{}, storageError(err)
	}
	if count >= domain.NetworkRulesMax {
		return domain.NetworkRule{}, domain.ErrConflict
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM libraries WHERE id=$1::uuid)`, in.LibraryID).Scan(&exists); err != nil {
		return domain.NetworkRule{}, storageError(err)
	}
	if !exists {
		return domain.NetworkRule{}, domain.ErrNotFound
	}
	var id string
	if err = tx.QueryRow(ctx, `INSERT INTO library_network_rules(library_id,network,cidrs,client_kinds,include_admins,enabled,note,created_by)
 VALUES($1::uuid,$2,$3::cidr[],$4::text[],$5,$6,$7,$8::uuid) RETURNING id::text`, in.LibraryID, in.Network, in.CIDRs, in.ClientKinds, in.IncludeAdmins, in.Enabled, in.Note, actor.UserID).Scan(&id); err != nil {
		return domain.NetworkRule{}, storageError(err)
	}
	v, err := scanNetworkRule(tx.QueryRow(ctx, `SELECT `+networkRuleColumns+networkRuleFrom+` WHERE r.id=$1::uuid`, id))
	if err != nil {
		return v, err
	}
	if err = appendAudit(ctx, tx, AuditEntry{Event: "access.network_rule_created", Actor: actor, TargetID: id, After: networkRuleAudit(v.NetworkRuleInput)}); err != nil {
		return domain.NetworkRule{}, err
	}
	return v, storageError(tx.Commit(ctx))
}

// UpdateNetworkRule replaces a rule; an unchanged rule is not audited.
func (s *Store) UpdateNetworkRule(ctx context.Context, actor domain.Actor, id string, in domain.NetworkRuleInput) (domain.NetworkRule, error) {
	if !domain.ValidID(id) {
		return domain.NetworkRule{}, domain.ErrNotFound
	}
	in, ok := normalizeNetworkRule(in)
	if !ok {
		return domain.NetworkRule{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.NetworkRule{}, err
	}
	defer tx.Rollback(ctx)
	old, err := scanNetworkRule(tx.QueryRow(ctx, `SELECT `+networkRuleColumns+networkRuleFrom+` WHERE r.id=$1::uuid FOR UPDATE OF r`, id))
	if err != nil {
		return old, err
	}
	if sameNetworkRule(old.NetworkRuleInput, in) {
		return old, storageError(tx.Commit(ctx))
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM libraries WHERE id=$1::uuid)`, in.LibraryID).Scan(&exists); err != nil {
		return domain.NetworkRule{}, storageError(err)
	}
	if !exists {
		return domain.NetworkRule{}, domain.ErrNotFound
	}
	if _, err = tx.Exec(ctx, `UPDATE library_network_rules SET library_id=$2::uuid,network=$3,cidrs=$4::cidr[],client_kinds=$5::text[],include_admins=$6,enabled=$7,note=$8,updated_at=now()
 WHERE id=$1::uuid`, id, in.LibraryID, in.Network, in.CIDRs, in.ClientKinds, in.IncludeAdmins, in.Enabled, in.Note); err != nil {
		return domain.NetworkRule{}, storageError(err)
	}
	v, err := scanNetworkRule(tx.QueryRow(ctx, `SELECT `+networkRuleColumns+networkRuleFrom+` WHERE r.id=$1::uuid`, id))
	if err != nil {
		return v, err
	}
	if err = appendAudit(ctx, tx, AuditEntry{Event: "access.network_rule_updated", Actor: actor, TargetID: id, Before: networkRuleAudit(old.NetworkRuleInput), After: networkRuleAudit(v.NetworkRuleInput)}); err != nil {
		return domain.NetworkRule{}, err
	}
	return v, storageError(tx.Commit(ctx))
}

func sameNetworkRule(a, b domain.NetworkRuleInput) bool {
	return a.LibraryID == b.LibraryID && a.Network == b.Network && slices.Equal(a.CIDRs, b.CIDRs) && slices.Equal(a.ClientKinds, b.ClientKinds) &&
		a.IncludeAdmins == b.IncludeAdmins && a.Enabled == b.Enabled && a.Note == b.Note
}

// DeleteNetworkRule removes a rule.
func (s *Store) DeleteNetworkRule(ctx context.Context, actor domain.Actor, id string) error {
	if !domain.ValidID(id) {
		return domain.ErrNotFound
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	old, err := scanNetworkRule(tx.QueryRow(ctx, `SELECT `+networkRuleColumns+networkRuleFrom+` WHERE r.id=$1::uuid FOR UPDATE OF r`, id))
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM library_network_rules WHERE id=$1::uuid`, id); err != nil {
		return storageError(err)
	}
	if err = appendAudit(ctx, tx, AuditEntry{Event: "access.network_rule_deleted", Actor: actor, TargetID: id, Before: networkRuleAudit(old.NetworkRuleInput)}); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}
