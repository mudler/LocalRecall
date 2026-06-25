package engine

import (
	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("applyConnTimeouts", func() {
	var config *pgxpool.Config

	BeforeEach(func() {
		var err error
		// A minimal but valid DSN so ParseConfig succeeds without a live DB.
		config, err = pgxpool.ParseConfig("postgres://u:p@localhost:5432/db")
		Expect(err).NotTo(HaveOccurred())
	})

	// noEnv simulates an environment where no override variables are set.
	noEnv := func(string) string { return "" }

	It("sets safe lock and idle timeouts by default", func() {
		applyConnTimeouts(config, noEnv)
		rp := config.ConnConfig.RuntimeParams
		Expect(rp).To(HaveKeyWithValue("lock_timeout", "30s"))
		Expect(rp).To(HaveKeyWithValue("idle_in_transaction_session_timeout", "300s"))
	})

	It("leaves statement_timeout unset by default (legitimate index builds may be long)", func() {
		applyConnTimeouts(config, noEnv)
		Expect(config.ConnConfig.RuntimeParams).NotTo(HaveKey("statement_timeout"))
	})

	It("honors env overrides for each timeout", func() {
		env := map[string]string{
			"POSTGRES_LOCK_TIMEOUT":                "5s",
			"POSTGRES_IDLE_IN_TRANSACTION_TIMEOUT": "60s",
			"POSTGRES_STATEMENT_TIMEOUT":           "90s",
		}
		applyConnTimeouts(config, func(k string) string { return env[k] })
		rp := config.ConnConfig.RuntimeParams
		Expect(rp).To(HaveKeyWithValue("lock_timeout", "5s"))
		Expect(rp).To(HaveKeyWithValue("idle_in_transaction_session_timeout", "60s"))
		Expect(rp).To(HaveKeyWithValue("statement_timeout", "90s"))
	})

	It("treats 0 or off as an explicit opt-out (does not set the param)", func() {
		env := map[string]string{
			"POSTGRES_LOCK_TIMEOUT":                "0",
			"POSTGRES_IDLE_IN_TRANSACTION_TIMEOUT": "off",
		}
		applyConnTimeouts(config, func(k string) string { return env[k] })
		rp := config.ConnConfig.RuntimeParams
		Expect(rp).NotTo(HaveKey("lock_timeout"))
		Expect(rp).NotTo(HaveKey("idle_in_transaction_session_timeout"))
	})

	It("does not panic when RuntimeParams is nil", func() {
		config.ConnConfig.RuntimeParams = nil
		Expect(func() { applyConnTimeouts(config, noEnv) }).NotTo(Panic())
		Expect(config.ConnConfig.RuntimeParams).To(HaveKey("lock_timeout"))
	})
})
