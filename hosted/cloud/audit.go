package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Audit actions, one per privileged cloud operation in r3 sections 10 and 15.
// The strings are stored in audit_events.action, so they are frozen once used.
// Add new ones, never rename or reuse an old one.
const (
	ActionAssociationCreated     = "association.created"
	ActionAssociationDisabled    = "association.disabled"
	ActionMemberAdded            = "member.added"
	ActionMemberRemoved          = "member.removed"
	ActionMemberRoleChanged      = "member.role_changed"
	ActionPairingApproved        = "pairing.approved"
	ActionPairingRejected        = "pairing.rejected"
	ActionTransferInitiated      = "transfer.initiated"
	ActionTransferAccepted       = "transfer.accepted"
	ActionTransferConfirmed      = "transfer.confirmed"
	ActionTransferExpired        = "transfer.expired"
	ActionTransferRejected       = "transfer.rejected"
	ActionSubscriptionTransition = "subscription.transition"
	ActionRelayAuthChanged       = "relay.authorization_changed"
)

// Denial reasons for relay authorization changes and denial metrics. The relay
// answers false for exactly these causes (r3 section 11.3 plus the standing
// conditions of an active association and a mirrored active key).
const (
	DenyMemberRemoved       = "member_removed"
	DenyRemoteDisabled      = "remote_disabled"
	DenySubscriptionLapsed  = "subscription_lapsed"
	DenyKeyRevoked          = "key_revoked"
	DenyAssociationInactive = "association_inactive"
	DenyUnknownKey          = "unknown_key"
)

// validActions is the closed set appendAudit accepts. A typo in a caller fails
// at write time rather than silently recording a row nothing will ever query.
var validActions = map[string]bool{
	ActionAssociationCreated: true, ActionAssociationDisabled: true,
	ActionMemberAdded: true, ActionMemberRemoved: true, ActionMemberRoleChanged: true,
	ActionPairingApproved: true, ActionPairingRejected: true,
	ActionTransferInitiated: true, ActionTransferAccepted: true,
	ActionTransferConfirmed: true, ActionTransferExpired: true,
	ActionTransferRejected:       true,
	ActionSubscriptionTransition: true,
	ActionRelayAuthChanged:       true,
}

// AuditDetails is the detail payload of one audit row. The field set is fixed:
// ids of accounts and devices, roles, statuses, and a reason token. There is
// no field that can hold a filesystem path, and validation below rejects path
// characters in every value. TestAuditPayloadCannotHoldAPath pins both.
type AuditDetails struct {
	AccountID  string `json:"account_id,omitempty"`
	DeviceKey  string `json:"device_key,omitempty"`
	Role       string `json:"role,omitempty"`
	PrevRole   string `json:"prev_role,omitempty"`
	FromStatus string `json:"from_status,omitempty"`
	ToStatus   string `json:"to_status,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// execer covers *pgxpool.Pool and pgx.Tx, so an emitter can run inside a
// feature's transaction or on its own.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// validateToken rejects anything that could carry a path. Ids in this system
// are UUIDs, hex keys, and short enum words; none of them contains a slash, a
// backslash, or a NUL byte. A filesystem path always contains one of those.
func validateToken(field, value string) error {
	if value == "" {
		return fmt.Errorf("audit: %s is empty", field)
	}
	if strings.ContainsAny(value, "/\\\x00") {
		return fmt.Errorf("audit: %s must not hold a path", field)
	}
	return nil
}

func validateOptionalToken(field, value string) error {
	if value == "" {
		return nil
	}
	return validateToken(field, value)
}

// appendAudit writes exactly one row. There is no update or delete path here,
// and migration 0003 adds a database trigger that rejects both.
func appendAudit(ctx context.Context, db execer, workspaceID, actor, action string, details AuditDetails) error {
	if !validActions[action] {
		return fmt.Errorf("audit: unknown action %q", action)
	}
	if err := validateToken("workspace_id", workspaceID); err != nil {
		return err
	}
	if err := validateToken("actor", actor); err != nil {
		return err
	}
	for field, value := range map[string]string{
		"account_id": details.AccountID, "device_key": details.DeviceKey,
		"role": details.Role, "prev_role": details.PrevRole,
		"from_status": details.FromStatus, "to_status": details.ToStatus,
		"reason": details.Reason,
	} {
		if err := validateOptionalToken(field, value); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("audit: marshal details: %w", err)
	}
	_, err = db.Exec(ctx,
		`INSERT INTO audit_events (workspace_id, actor, action, details)
		 VALUES ($1, $2, $3, $4)`,
		workspaceID, actor, action, string(raw))
	if err != nil {
		return fmt.Errorf("audit: insert %s: %w", action, err)
	}
	return nil
}

// RecordAssociationCreated logs an Enable Remote Access that won its race
// (r3 section 10.1). Call it in the same transaction as the association row.
func RecordAssociationCreated(ctx context.Context, db execer, workspaceID, actor string) error {
	return appendAudit(ctx, db, workspaceID, actor, ActionAssociationCreated, AuditDetails{})
}

// RecordAssociationDisabled logs a Disable Remote Access (r3 section 10.4),
// by the owner or by an admin device. Reason names which, for example
// "owner_request" or "admin_request".
func RecordAssociationDisabled(ctx context.Context, db execer, workspaceID, actor, reason string) error {
	return appendAudit(ctx, db, workspaceID, actor, ActionAssociationDisabled,
		AuditDetails{Reason: reason})
}

// RecordMemberAdded logs the owner or a manager admitting an account.
func RecordMemberAdded(ctx context.Context, db execer, workspaceID, actor, accountID, role string) error {
	return appendAudit(ctx, db, workspaceID, actor, ActionMemberAdded,
		AuditDetails{AccountID: accountID, Role: role})
}

// RecordMemberRemoved logs the owner or a manager removing an account.
func RecordMemberRemoved(ctx context.Context, db execer, workspaceID, actor, accountID string) error {
	return appendAudit(ctx, db, workspaceID, actor, ActionMemberRemoved,
		AuditDetails{AccountID: accountID})
}

// RecordMemberRoleChanged logs a manager to member demotion or the reverse.
func RecordMemberRoleChanged(ctx context.Context, db execer, workspaceID, actor, accountID, prevRole, role string) error {
	return appendAudit(ctx, db, workspaceID, actor, ActionMemberRoleChanged,
		AuditDetails{AccountID: accountID, PrevRole: prevRole, Role: role})
}

// RecordPairingApproved logs a cloud-side approval of a pending device.
func RecordPairingApproved(ctx context.Context, db execer, workspaceID, actor, deviceKey, accountID string) error {
	return appendAudit(ctx, db, workspaceID, actor, ActionPairingApproved,
		AuditDetails{DeviceKey: deviceKey, AccountID: accountID})
}

// RecordPairingRejected logs a cloud-side rejection of a pending device.
func RecordPairingRejected(ctx context.Context, db execer, workspaceID, actor, deviceKey, accountID string) error {
	return appendAudit(ctx, db, workspaceID, actor, ActionPairingRejected,
		AuditDetails{DeviceKey: deviceKey, AccountID: accountID})
}

// RecordTransferInitiated logs the owner naming a target account.
func RecordTransferInitiated(ctx context.Context, db execer, workspaceID, actor, toAccountID string) error {
	return appendAudit(ctx, db, workspaceID, actor, ActionTransferInitiated,
		AuditDetails{AccountID: toAccountID})
}

// RecordTransferAccepted logs the target account accepting.
func RecordTransferAccepted(ctx context.Context, db execer, workspaceID, actor string) error {
	return appendAudit(ctx, db, workspaceID, actor, ActionTransferAccepted, AuditDetails{})
}

// RecordTransferConfirmed logs the in-app confirmation by an admin device.
func RecordTransferConfirmed(ctx context.Context, db execer, workspaceID, actor string) error {
	return appendAudit(ctx, db, workspaceID, actor, ActionTransferConfirmed, AuditDetails{})
}

// RecordTransferExpired logs the 7-day request lapsing with no confirmation.
// The actor is the expiring job; pass "system" from the job.
func RecordTransferExpired(ctx context.Context, db execer, workspaceID, actor string) error {
	return appendAudit(ctx, db, workspaceID, actor, ActionTransferExpired, AuditDetails{})
}

// RecordTransferRejected logs the target account declining.
func RecordTransferRejected(ctx context.Context, db execer, workspaceID, actor string) error {
	return appendAudit(ctx, db, workspaceID, actor, ActionTransferRejected, AuditDetails{})
}

// RecordSubscriptionTransition logs a subscription status change for the
// account that pays for the workspace. WorkspaceID scopes the affected
// workspace; when the account has no association the caller passes the account
// id so the row keeps its required scope.
func RecordSubscriptionTransition(ctx context.Context, db execer, workspaceID, actor, accountID, fromStatus, toStatus string) error {
	return appendAudit(ctx, db, workspaceID, actor, ActionSubscriptionTransition,
		AuditDetails{AccountID: accountID, FromStatus: fromStatus, ToStatus: toStatus})
}

// RecordRelayAuthChanged logs a device key gaining or losing relay use, with
// the reason from the Deny* set above or "subscription_renewed",
// "member_added", and similar grants.
func RecordRelayAuthChanged(ctx context.Context, db execer, workspaceID, actor, deviceKey, fromStatus, toStatus, reason string) error {
	return appendAudit(ctx, db, workspaceID, actor, ActionRelayAuthChanged,
		AuditDetails{DeviceKey: deviceKey, FromStatus: fromStatus, ToStatus: toStatus, Reason: reason})
}
