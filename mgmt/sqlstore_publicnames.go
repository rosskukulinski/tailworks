package mgmt

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Public-name ownership (red-team H10), SQL side. The table is
// migrations/202611152337_public_names.sql.

var _ publicNameStore = (*SQLStore)(nil)

const publicNameCols = "tailnet, name, tenant_id, claimed_at, claimed_by"

func scanPublicName(sc interface{ Scan(...any) error }) (PublicNameClaim, error) {
	var c PublicNameClaim
	err := sc.Scan(&c.Tailnet, &c.Name, &c.TenantID, &c.ClaimedAt, &c.ClaimedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// errClaimExists rolls back a claim that found the name already owned, so
// the claim's audit intent commits only with a new row.
var errClaimExists = errors.New("public name already claimed")

// ClaimPublicName records c unless (c.Tailnet, c.Name) is already owned. It
// returns the owner (c, or the existing claim) and whether c was recorded.
func (s *SQLStore) ClaimPublicName(ctx context.Context, c PublicNameClaim) (PublicNameClaim, bool, error) {
	var cur PublicNameClaim
	err := s.tx(ctx, func(tx *sql.Tx) error {
		got, err := scanPublicName(tx.QueryRowContext(ctx, s.q("SELECT "+publicNameCols+" FROM public_names WHERE tailnet=? AND name=?"), c.Tailnet, c.Name))
		if err == nil {
			cur = got
			return errClaimExists
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		_, err = tx.ExecContext(ctx, s.q("INSERT INTO public_names ("+publicNameCols+") VALUES (?,?,?,?,?)"),
			c.Tailnet, c.Name, c.TenantID, c.ClaimedAt, c.ClaimedBy)
		return err
	})
	switch {
	case errors.Is(err, errClaimExists):
		return cur, false, nil
	case err != nil:
		// A concurrent claim of the same name won the primary key.
		if got, gerr := s.GetPublicName(ctx, c.Tailnet, c.Name); gerr == nil {
			return got, false, nil
		}
		return PublicNameClaim{}, false, err
	}
	return c, true, nil
}

// GetPublicName returns the owner of a public name (ErrNotFound: unclaimed).
func (s *SQLStore) GetPublicName(ctx context.Context, tailnet, name string) (PublicNameClaim, error) {
	return scanPublicName(s.db.QueryRowContext(ctx, s.q("SELECT "+publicNameCols+" FROM public_names WHERE tailnet=? AND name=?"), tailnet, name))
}

// ListPublicNames lists a tenant's claims ordered by (claimed_at, name).
func (s *SQLStore) ListPublicNames(ctx context.Context, tenantID string, pg Page) ([]PublicNameClaim, error) {
	where := []string{"tenant_id=?"}
	args := []any{tenantID}
	if pg.After != nil {
		where = append(where, "(claimed_at > ? OR (claimed_at = ? AND name > ?))")
		args = append(args, pg.After.Created, pg.After.Created, pg.After.ID)
	}
	q := "SELECT " + publicNameCols + " FROM public_names WHERE " + strings.Join(where, " AND ") + " ORDER BY claimed_at, name" + limitClause(pg)
	rows, err := s.db.QueryContext(ctx, s.q(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PublicNameClaim
	for rows.Next() {
		c, err := scanPublicName(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReleasePublicName deletes tenantID's claim of a name (ErrNotFound: the
// tenant does not own it).
func (s *SQLStore) ReleasePublicName(ctx context.Context, tailnet, name, tenantID string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, s.q("DELETE FROM public_names WHERE tailnet=? AND name=? AND tenant_id=?"), tailnet, name, tenantID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}
