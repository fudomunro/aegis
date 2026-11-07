package types

// Data-ownership confirmation (issue #165): the user's statement that Aegis
// already holds all the data of one library kind they care about. Until a
// kind is confirmed, the admin surface keeps a call to action to bring more
// data of that kind under management.
//
// Distinct from connection maturity (issue #247). Maturity grades how
// completely one connector covers the types its service offers; ownership
// tracks how completely the user's data of one type is under management —
// which sources are in use, which declared routes are still unused, and
// whether the user has confirmed the collection is complete.

// OwnershipEntry is the user's confirmation for one library kind.
type OwnershipEntry struct {
	// Confirmed is the user's statement that Aegis holds all the data of
	// this kind they care about. False (or absent) keeps the call to action.
	Confirmed bool `json:"confirmed"`
	// ConfirmedAt is when the user confirmed (RFC3339). Empty when not
	// confirmed or when the entry was reopened.
	ConfirmedAt string `json:"confirmed_at,omitempty"`
	// Note is an optional user note ("work email only", …).
	Note string `json:"note,omitempty"`
}

// OwnershipSource is one source currently feeding a kind.
type OwnershipSource struct {
	ServiceID    string `json:"service_id"`
	Name         string `json:"name"`
	Role         string `json:"role,omitempty"`
	Count        int    `json:"count,omitempty"`
	LastSyncedAt string `json:"last_synced_at,omitempty"`
	// Detail is a short evidence phrase ("cursor done", "250 rows imported").
	Detail string `json:"detail,omitempty"`
}

// OwnershipAction names where the user starts to bring more data under
// management. Declared beside the connector or ingest source it describes
// (issue #254) — core reads the declaration and never derives it from
// interfaces or transport class.
type OwnershipAction struct {
	// Kind is "connect" (OAuth login), "setup" (onboarding flow), or
	// "import" (local import panel).
	Kind string `json:"kind"`
	// Target is the setup id or admin panel anchor. Empty means the
	// source's service id.
	Target string `json:"target,omitempty"`
	// Label is the call-to-action phrase shown to the user
	// ("Import Signal chats"). Provider copy lives beside the connector.
	Label string `json:"label"`
}

// Action kinds an OwnershipAction may declare. Pinned against the real
// seams: "setup" connectors implement Onboarder, "import" sources expose an
// import panel, "connect" is the OAuth login.
const (
	OwnershipActionConnect = "connect"
	OwnershipActionSetup   = "setup"
	OwnershipActionImport  = "import"
)

// OwnershipRoute is a declared way to get more data of a kind that is not
// yet in use. Routes come from the connection-maturity catalog and the
// ingest-source declarations — never from a hardcoded per-kind list.
type OwnershipRoute struct {
	ServiceID   string          `json:"service_id"`
	Name        string          `json:"name"`
	Transport   Transport       `json:"transport"`
	Description string          `json:"description,omitempty"`
	Action      OwnershipAction `json:"action"`
}

// OwnershipFreshness is the copy-freshness rollup for one kind (issue #227),
// passed through so the ownership view can say "3 sources, 1 stale".
type OwnershipFreshness struct {
	InSync      int    `json:"in_sync"`
	Stale       int    `json:"stale"`
	Missing     int    `json:"missing"`
	Unchecked   int    `json:"unchecked"`
	Total       int    `json:"total"`
	LastChecked string `json:"last_checked_at,omitempty"`
}

// OwnershipStatus is the derived + confirmed ownership state for one
// library kind. Confirmed is the only user-authored field; everything else
// is evidence computed from configured services, local stores, the mail
// archive, and the copy ledger.
type OwnershipStatus struct {
	Kind        string             `json:"kind"`
	Label       string             `json:"label"`
	Confirmed   bool               `json:"confirmed"`
	ConfirmedAt string             `json:"confirmed_at,omitempty"`
	Note        string             `json:"note,omitempty"`
	// UnderManagement lists sources currently feeding this kind.
	UnderManagement []OwnershipSource `json:"under_management"`
	// UnusedRoutes lists declared ways to get more data of this kind that
	// are not yet in use. Empty is not "complete" — only Confirmed is.
	UnusedRoutes []OwnershipRoute    `json:"unused_routes"`
	Freshness    *OwnershipFreshness `json:"freshness,omitempty"`
}
