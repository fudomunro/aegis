package shared

// SyncRelationship is one source's pairing with one library kind (issue #365):
// established when the source is connected (remote service or local file
// import) and stored with the connection. Canonical marks this member as the
// kind's sync-group hub — the designated canonical home. ReadOnly marks
// members that never receive writes (local file imports). Members lists the
// service IDs in the kind's group (view data, filled by the reader).
type SyncRelationship struct {
	Kind      string   `json:"kind"`
	Canonical bool     `json:"canonical,omitempty"`
	ReadOnly  bool     `json:"read_only,omitempty"`
	Members   []string `json:"members,omitempty"`
}
