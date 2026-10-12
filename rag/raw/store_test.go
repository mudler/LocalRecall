package raw

import (
	"errors"
	"testing"
)

func TestRawErrorStatuses(t *testing.T) {
	for _, tc := range []struct {
		code      string
		status    int
		retryable bool
	}{
		{CodeValidation, 400, false}, {CodeCursor, 400, false}, {CodeUnauthorized, 401, false},
		{CodeNotFound, 404, false}, {CodeConflict, 409, false}, {CodeSpaceMismatch, 409, false},
		{CodeBodyLimit, 413, false}, {CodeDeleteBound, 422, false}, {CodeUnsupported, 501, false},
		{CodeUnavailable, 503, true}, {CodeTimeout, 504, true}, {CodeInternal, 500, false},
	} {
		t.Run(tc.code, func(t *testing.T) {
			e := NewError(tc.code)
			if e.Code != tc.code || e.Status != tc.status || e.Retryable != tc.retryable || e.Error() == "" {
				t.Fatal(e)
			}
		})
	}
	e := NewError("secret database error")
	if e.Code != CodeInternal || e.Error() != "Internal error" {
		t.Fatal(e)
	}
}

func TestRawErrorClassification(t *testing.T) {
	if !errors.Is(NewError(CodeTimeout), NewError(CodeTimeout)) {
		t.Fatal("classification lost")
	}
	if errors.Is(NewError(CodeTimeout), NewError(CodeUnavailable)) {
		t.Fatal("different codes match")
	}
}
