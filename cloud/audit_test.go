package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The cloud side of the r3 section 15 authority matrix. Each entry is one
// privileged action the cloud participates in, the emitter that records it,
// and the stored action string. Every entry must produce exactly one row.
func TestAuditAuthorityMatrixActions(t *testing.T) {
	pool := freshDB(t, 4)
	ctx := t.Context()

	actor := "11111111-1111-1111-1111-111111111111"
	other := "22222222-2222-2222-2222-222222222222"
	device := "dev-key-hex-01"

	type actionCase struct {
		name   string
		record func(ws string) error
		action string
	}
	cases := []actionCase{
		{"enable remote access creates an association",
			func(ws string) error { return RecordAssociationCreated(ctx, pool, ws, actor) },
			ActionAssociationCreated},
		{"disable remote access by owner or admin",
			func(ws string) error { return RecordAssociationDisabled(ctx, pool, ws, actor, "owner_request") },
			ActionAssociationDisabled},
		{"owner or manager adds a member",
			func(ws string) error { return RecordMemberAdded(ctx, pool, ws, actor, other, "member") },
			ActionMemberAdded},
		{"owner or manager removes a member",
			func(ws string) error { return RecordMemberRemoved(ctx, pool, ws, actor, other) },
			ActionMemberRemoved},
		{"owner or manager changes a member role",
			func(ws string) error {
				return RecordMemberRoleChanged(ctx, pool, ws, actor, other, "member", "manager")
			},
			ActionMemberRoleChanged},
		{"owner or manager approves a pairing",
			func(ws string) error { return RecordPairingApproved(ctx, pool, ws, actor, device, other) },
			ActionPairingApproved},
		{"owner or manager rejects a pairing",
			func(ws string) error { return RecordPairingRejected(ctx, pool, ws, actor, device, other) },
			ActionPairingRejected},
		{"owner initiates a transfer",
			func(ws string) error { return RecordTransferInitiated(ctx, pool, ws, actor, other) },
			ActionTransferInitiated},
		{"target accepts a transfer",
			func(ws string) error { return RecordTransferAccepted(ctx, pool, ws, other) },
			ActionTransferAccepted},
		{"admin device confirms a transfer",
			func(ws string) error { return RecordTransferConfirmed(ctx, pool, ws, device) },
			ActionTransferConfirmed},
		{"transfer request expires",
			func(ws string) error { return RecordTransferExpired(ctx, pool, ws, "system") },
			ActionTransferExpired},
		{"target rejects a transfer",
			func(ws string) error { return RecordTransferRejected(ctx, pool, ws, other) },
			ActionTransferRejected},
		{"subscription changes state",
			func(ws string) error {
				return RecordSubscriptionTransition(ctx, pool, ws, "system", actor, "active", "grace")
			},
			ActionSubscriptionTransition},
		{"relay authorization changes",
			func(ws string) error {
				return RecordRelayAuthChanged(ctx, pool, ws, "system", device, "active", "revoked", DenyMemberRemoved)
			},
			ActionRelayAuthChanged},
	}

	for _, c := range cases {
		ws := "ws-audit-" + strings.ReplaceAll(c.action, ".", "-")
		if err := c.record(ws); err != nil {
			t.Errorf("%s: record: %v", c.name, err)
			continue
		}
		var n int
		var gotAction, gotActor, gotWS string
		err := pool.QueryRow(ctx,
			`SELECT count(*) OVER (), action, actor, workspace_id FROM audit_events WHERE workspace_id = $1`,
			ws).Scan(&n, &gotAction, &gotActor, &gotWS)
		if err != nil {
			t.Errorf("%s: read back: %v", c.name, err)
			continue
		}
		if n != 1 {
			t.Errorf("%s: %d audit rows, want exactly 1", c.name, n)
		}
		if gotAction != c.action {
			t.Errorf("%s: action = %q, want %q", c.name, gotAction, c.action)
		}
		if gotWS != ws {
			t.Errorf("%s: workspace = %q, want %q", c.name, gotWS, ws)
		}
		if gotActor == "" {
			t.Errorf("%s: actor is empty", c.name)
		}
	}

	var total int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events`).Scan(&total); err != nil {
		t.Fatalf("count all: %v", err)
	}
	if total != len(cases) {
		t.Errorf("%d audit rows total, want %d (one per matrix action)", total, len(cases))
	}
}

// History must survive its writers. The trigger from 0003 rejects updates and
// deletes; this test proves the trigger is installed, not just written.
func TestAuditLogIsAppendOnly(t *testing.T) {
	pool := freshDB(t, 4)
	ctx := t.Context()

	if err := RecordMemberAdded(ctx, pool, "ws-append", "actor-1", "acc-1", "member"); err != nil {
		t.Fatalf("record: %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE audit_events SET action = 'tampered'`); err == nil {
		t.Error("UPDATE audit_events succeeded, want the append-only trigger to reject it")
	} else if !strings.Contains(err.Error(), "append-only") {
		t.Errorf("UPDATE failed with %v, want the append-only error", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM audit_events`); err == nil {
		t.Error("DELETE audit_events succeeded, want the append-only trigger to reject it")
	} else if !strings.Contains(err.Error(), "append-only") {
		t.Errorf("DELETE failed with %v, want the append-only error", err)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("%d rows after rejected writes, want the original 1", n)
	}
}

// The hard constraint: the cloud never receives a filesystem path (r3 section
// 7), and the audit log is where one would leak in by accident. Three locks:
// the detail struct has a fixed field set with no path-shaped name, every
// value rejects path characters, and a serialized payload never contains one.
func TestAuditPayloadCannotHoldAPath(t *testing.T) {
	wantFields := []string{"account_id", "device_key", "role", "prev_role", "from_status", "to_status", "reason"}
	gotFields := []string{}
	typ := reflect.TypeOf(AuditDetails{})
	for i := range typ.NumField() {
		tag := typ.Field(i).Tag.Get("json")
		name := strings.TrimSuffix(tag, ",omitempty")
		gotFields = append(gotFields, name)
		lower := strings.ToLower(typ.Field(i).Name + " " + name)
		for _, banned := range []string{"path", "file", "dir", "root"} {
			if strings.Contains(lower, banned) {
				t.Errorf("AuditDetails field %q looks able to hold a path", typ.Field(i).Name)
			}
		}
	}
	if !reflect.DeepEqual(gotFields, wantFields) {
		t.Errorf("AuditDetails fields = %v, want %v; a new field needs a path review", gotFields, wantFields)
	}

	paths := []string{
		"/etc/passwd", "a/b", `C:\keys\x`, "../escape", "ws/../x",
		"share/file.txt", "x\x00y", "/",
	}
	for _, p := range paths {
		if err := appendAudit(t.Context(), nil, p, "actor", ActionMemberAdded, AuditDetails{}); err == nil {
			t.Errorf("workspace %q accepted, want rejection", p)
		}
		if err := appendAudit(t.Context(), nil, "ws", p, ActionMemberAdded, AuditDetails{}); err == nil {
			t.Errorf("actor %q accepted, want rejection", p)
		}
		for field, fill := range map[string]func(*AuditDetails){
			"account_id":  func(d *AuditDetails) { d.AccountID = p },
			"device_key":  func(d *AuditDetails) { d.DeviceKey = p },
			"role":        func(d *AuditDetails) { d.Role = p },
			"prev_role":   func(d *AuditDetails) { d.PrevRole = p },
			"from_status": func(d *AuditDetails) { d.FromStatus = p },
			"to_status":   func(d *AuditDetails) { d.ToStatus = p },
			"reason":      func(d *AuditDetails) { d.Reason = p },
		} {
			var det AuditDetails
			fill(&det)
			if err := appendAudit(t.Context(), nil, "ws", "actor", ActionMemberAdded, det); err == nil {
				t.Errorf("details.%s = %q accepted, want rejection", field, p)
			}
		}
	}

	// Unknown actions and empty identifiers fail before any database is touched.
	if err := appendAudit(t.Context(), nil, "ws", "actor", "remote_access.enabled-typo", AuditDetails{}); err == nil {
		t.Error("unknown action accepted, want rejection")
	}
	if err := appendAudit(t.Context(), nil, "", "actor", ActionMemberAdded, AuditDetails{}); err == nil {
		t.Error("empty workspace accepted, want rejection")
	}

	// Representative payloads serialize with no path separator anywhere.
	valid := []AuditDetails{
		{},
		{AccountID: "11111111-1111-1111-1111-111111111111", Role: "manager"},
		{DeviceKey: "abcdef0123456789", AccountID: "22222222-2222-2222-2222-222222222222"},
		{FromStatus: "active", ToStatus: "suspended", Reason: DenySubscriptionLapsed},
		{PrevRole: "member", Role: "manager"},
	}
	for _, det := range valid {
		raw, err := json.Marshal(det)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.ContainsAny(string(raw), "/\\\x00") {
			t.Errorf("payload %s contains a path character", raw)
		}
	}
}
