package contracts

import "time"

type DueCursor struct {
	At time.Time
	ID int64
}

type DueTask struct {
	DueCursor
	CampusID int64
	Round    int
}
