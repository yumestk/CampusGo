package fault

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"strings"

	"github.com/go-sql-driver/mysql"

	"github.com/yumestk/CampusGo/internal/failure"
)

type Error = failure.Error

var New = failure.New

// Only known availability failures are retryable. Schema, credential and code errors remain 500.
func Dependency(err error) error {
	if err == nil {
		return nil
	}
	var network net.Error
	var db *mysql.MySQLError
	transient := errors.As(err, &network) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) || errors.Is(err, mysql.ErrInvalidConn) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
	if errors.As(err, &db) {
		switch db.Number {
		case 1040, 1042, 1053, 1205, 1213, 2006, 2013:
			transient = true
		}
	}
	for _, prefix := range []string{"LOADING ", "BUSY ", "TRYAGAIN ", "CLUSTERDOWN ", "MASTERDOWN ", "READONLY ", "OOM ", "redis: connection pool timeout"} {
		transient = transient || strings.HasPrefix(err.Error(), prefix)
	}
	if transient {
		return &Error{Code: "DEPENDENCY_UNAVAILABLE", Status: 503, Cause: err}
	}
	return &Error{Code: "INTERNAL_ERROR", Status: 500, Cause: err}
}
