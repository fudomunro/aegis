package shared

// SyncProjectionFreeBusy is the events copy projection (issue #145): copies
// placed on this member are projected to busy blocks — times only, no title,
// description, attendees, or location. Cross-service identity (iCal UID) is
// preserved so re-runs match instead of duplicating.
const SyncProjectionFreeBusy = "freebusy"

// SyncRelationship is one source's pairing with one library kind (issue #365):
// established when the source is connected (remote service or local file
// import) and stored with the connection. Canonical marks this member as the
// kind's sync-group hub — the designated canonical home. ReadOnly marks
// members that never receive writes (local file imports). Members lists the
// service IDs in the kind's group (view data, filled by the reader).
// Projection narrows what copies placed on this member carry (issue #145):
// SyncProjectionFreeBusy for events, empty for a full copy.
type SyncRelationship struct {
	Kind       string   `json:"kind"`
	Canonical  bool     `json:"canonical,omitempty"`
	ReadOnly   bool     `json:"read_only,omitempty"`
	Projection string   `json:"projection,omitempty"`
	Members    []string `json:"members,omitempty"`
}
