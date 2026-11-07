package application

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/yumestk/CampusGo/internal/contracts"
	walletdomain "github.com/yumestk/CampusGo/internal/domain/wallet"
)

func DecodeFundEvent(body []byte) (FundEvent, error) {
	var e FundEvent
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if len(body) > 4096 || d.Decode(&e) != nil || d.Decode(new(any)) != io.EOF || !contracts.ValidRequestKey(e.RequestID) || e.Amount <= 0 || e.CampusID <= 0 || e.ErrandID <= 0 || e.PublisherID <= 0 || e.Round < 0 || !validFundCommandID(e) {
		return e, fmt.Errorf("invalid funds event")
	}
	return e, nil
}
func validFundCommandID(e FundEvent) bool {
	switch e.Kind {
	case "CANCEL":
		return e.ActorID == e.PublisherID && e.Commission == 0
	case "ARBITRATE_PUBLISHER":
		return e.ActorID > 0 && e.RunnerID > 0 && e.Commission == 0
	case "SETTLE":
		return e.ActorID == e.PublisherID && e.RunnerID > 0 && e.Commission == walletdomain.Commission(e.Amount)
	case "AUTO_SETTLE":
		return e.ActorID == -1 && e.RunnerID > 0 && e.Commission == walletdomain.Commission(e.Amount)
	case "ARBITRATE_RUNNER":
		return e.ActorID > 0 && e.RunnerID > 0 && e.Commission == walletdomain.Commission(e.Amount)
	}
	return false
}
