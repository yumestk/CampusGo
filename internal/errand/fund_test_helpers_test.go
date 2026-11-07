package errand

import (
	"context"
	"database/sql"
	"github.com/yumestk/CampusGo/internal/application"
	"github.com/yumestk/CampusGo/internal/auth"
)

type testFunds struct {
	*application.Funds
	store *FundStore
}

func newTestFunds(g *Grabber, send func(context.Context, FundEvent) (func(bool) error, error)) *testFunds {
	store := &FundStore{DB: g.DB, IDs: g.IDs}
	return &testFunds{&application.Funds{Store: store, Send: send}, store}
}
func (f *testFunds) execute(ctx context.Context, u auth.User, key string, c FundCommand, before, commit func(*sql.Tx) error) (FundResult, error) {
	store := *f.store
	store.beforeCommit = before
	store.commit = commit
	app := *f.Funds
	app.Store = &store
	return app.Execute(ctx, u, key, c)
}

func (g *Grabber) ScanAutoSettle(ctx context.Context, f *testFunds) error {
	return f.ScanAutoSettle(ctx)
}
