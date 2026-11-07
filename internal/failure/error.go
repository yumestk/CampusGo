package failure

type Error struct {
	Code             string
	Status           int
	Cause            error
	RetryAfterMillis int64
}

func (e *Error) Error() string          { return e.Code }
func (e *Error) Unwrap() error          { return e.Cause }
func New(code string, status int) error { return &Error{Code: code, Status: status} }
