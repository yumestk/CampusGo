package fault

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestDependencyClassification(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{fmt.Errorf("query: %w", context.DeadlineExceeded), 503},
		{io.EOF, 503},
		{io.ErrUnexpectedEOF, 503},
		{&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}, 503},
		{&mysql.MySQLError{Number: 1213}, 503},
		{&mysql.MySQLError{Number: 1146}, 500},
		{errors.New("WRONGPASS credentials"), 500},
		{errors.New("BUSY Redis is running a script"), 503},
		{errors.New("LOADING Redis is loading the dataset"), 503},
		{errors.New("TRYAGAIN temporary failure"), 503},
		{errors.New("WRONGTYPE value type"), 500},
		{errors.New("corrupt session"), 500},
	} {
		var result *Error
		if !errors.As(Dependency(tc.err), &result) || result.Status != tc.status {
			t.Fatalf("%v: %+v", tc.err, result)
		}
	}
}
