package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// seedSQL writes one workspace's worth of development fixture. Ids are fixed rather than
// generated so that re-running the command is a no-op and so that a developer can paste an
// id into a request without looking it up first.
//
// It is not a substitute for a test fixture: tests build their own rows.
const seedSQL = `
INSERT INTO accounts (id, email) VALUES
    ('00000000-0000-0000-0000-000000000001', 'owner@example.test'),
    ('00000000-0000-0000-0000-000000000002', 'member@example.test')
ON CONFLICT (id) DO NOTHING;

INSERT INTO subscriptions (account_id, tier, status, current_period_end) VALUES
    ('00000000-0000-0000-0000-000000000001', 'standard', 'active', now() + interval '30 days')
ON CONFLICT (account_id) DO NOTHING;

INSERT INTO workspace_associations (workspace_id, owner_account_id, status, trust_list_version) VALUES
    ('ws-dev', '00000000-0000-0000-0000-000000000001', 'active', 1)
ON CONFLICT (workspace_id) DO NOTHING;

INSERT INTO workspace_members (workspace_id, account_id, role, status) VALUES
    ('ws-dev', '00000000-0000-0000-0000-000000000001', 'owner', 'active'),
    ('ws-dev', '00000000-0000-0000-0000-000000000002', 'member', 'active')
ON CONFLICT (workspace_id, account_id) DO NOTHING;

INSERT INTO devices (device_key, account_id, display_name) VALUES
    ('dev-admin-laptop', '00000000-0000-0000-0000-000000000001', 'Owner laptop'),
    ('dev-member-desktop', '00000000-0000-0000-0000-000000000002', 'Member desktop'),
    ('dev-unbound-nas', NULL, 'Locally paired NAS')
ON CONFLICT (device_key) DO NOTHING;

-- A mirror of what the signed trust list on those devices already says. The revoked row is
-- here so that a relay authorization denial can be exercised locally.
INSERT INTO device_authorizations (workspace_id, device_key, status) VALUES
    ('ws-dev', 'dev-admin-laptop', 'active'),
    ('ws-dev', 'dev-member-desktop', 'active'),
    ('ws-dev', 'dev-unbound-nas', 'revoked')
ON CONFLICT (workspace_id, device_key) DO NOTHING;

INSERT INTO audit_events (workspace_id, actor, action)
SELECT 'ws-dev', '00000000-0000-0000-0000-000000000001', '` + ActionAssociationCreated + `'
WHERE NOT EXISTS (SELECT 1 FROM audit_events WHERE workspace_id = 'ws-dev');
`

func seed(ctx context.Context, db *pgxpool.Pool, log *slog.Logger) error {
	err := inTx(ctx, db, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, seedSQL)
		return err
	})
	if err != nil {
		return err
	}
	log.Info("seeded", "workspace_id", "ws-dev", "owner", "owner@example.test")
	return nil
}
