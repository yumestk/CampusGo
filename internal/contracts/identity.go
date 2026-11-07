package contracts

// Actor is an authenticated identity snapshot, not a credential service. Writes
// revalidate the relevant identity fields under their existing database locks.
type Actor struct {
	ID       int64  `json:"id,string"`
	CampusID int64  `json:"campusId,string"`
	Name     string `json:"name"`
	Role     string `json:"role"`
}
