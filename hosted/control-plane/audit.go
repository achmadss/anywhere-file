package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Audit actions, one per privileged cloud operation. The strings are stored in
// audit_events.action, so they are frozen once used. Add new ones, never rename or reuse
// an old one. The actions of the new model land with their features (#89).
const (
	ActionAccountDeleted = "account.deleted"
)

// validActions is the closed set appendAudit accepts. A typo in a caller fails
// at write time rather than silently recording a row nothing will ever query.
var validActions = map[string]bool{
	ActionAccountDeleted: true,
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

// appendAudit writes exactly one row. deviceID may be empty for actions on an account
// alone. There is no update or delete path here, and the schema adds a trigger that
// rejects both.
func appendAudit(ctx context.Context, db execer, deviceID, actor, action string, details AuditDetails) error {
	if !validActions[action] {
		return fmt.Errorf("audit: unknown action %q", action)
	}
	if err := validateOptionalToken("device_id", deviceID); err != nil {
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
		`INSERT INTO audit_events (device_id, actor, action, details)
		 VALUES (NULLIF($1, ''), $2, $3, $4)`,
		deviceID, actor, action, string(raw))
	if err != nil {
		return fmt.Errorf("audit: insert %s: %w", action, err)
	}
	return nil
}
