package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mudler/localrecall/rag/raw"
)

func TestDBErrorSafeClassification(t *testing.T) {
	secret := "SQL password=secret caller-payload"
	for _, tc := range []struct {
		name               string
		cause              error
		code               string
		canceled, deadline bool
	}{
		{"cancel", fmt.Errorf("%s: %w", secret, context.Canceled), raw.CodeTimeout, true, false},
		{"deadline", fmt.Errorf("%s: %w", secret, context.DeadlineExceeded), raw.CodeTimeout, false, true},
		{"both", errors.Join(context.Canceled, context.DeadlineExceeded), raw.CodeTimeout, true, true},
		{"statement", &pgconn.PgError{Code: "57014", Message: secret}, raw.CodeTimeout, false, false},
		{"lock", &pgconn.PgError{Code: "55P03", Detail: secret}, raw.CodeTimeout, false, false},
		{"driver", &pgconn.PgError{Code: "08006", Message: secret}, raw.CodeUnavailable, false, false},
		{"unknown code", &raw.Error{Code: secret, Message: secret}, raw.CodeInternal, false, false},
		{"tainted raw", &raw.Error{Code: raw.CodeValidation, Message: secret}, raw.CodeValidation, false, false},
		{"unknown", errors.New(secret), raw.CodeUnavailable, false, false},
		{"raw wrapped", fmt.Errorf("%s: %w", secret, raw.NewError(raw.CodeValidation)), raw.CodeValidation, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := dbError(tc.cause)
			var re *raw.Error
			if !errors.As(err, &re) || re.Code != tc.code {
				t.Fatalf("classification: %v", err)
			}
			if !errors.Is(err, raw.NewError(tc.code)) {
				t.Error("safe classification identity lost")
			}
			if errors.Is(err, context.Canceled) != tc.canceled || errors.Is(err, context.DeadlineExceeded) != tc.deadline {
				t.Error("context identity lost")
			}
			var driver *pgconn.PgError
			if errors.As(err, &driver) {
				t.Error("driver exposed")
			}
			b, e := json.Marshal(err)
			if e != nil {
				t.Fatal(e)
			}
			var log bytes.Buffer
			slog.New(slog.NewJSONHandler(&log, nil)).Error("operation", "error", err)
			for _, output := range []string{err.Error(), fmt.Sprintf("%+v %#v", err, err), string(b), log.String()} {
				if strings.Contains(output, secret) {
					t.Errorf("unsafe diagnostic: %s", output)
				}
			}
		})
	}
	if dbError(nil) != nil {
		t.Fatal("nil changed")
	}
}
