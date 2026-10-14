package collect

import (
	"testing"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/gitcli"
)

// This file holds what the collectors' tests share. They run real git,
// never a mock (docs/spec/codebase.md#tests). If package collect ever needs
// a TestMain, it goes here, so two branches cannot each add one.

// newRunner returns the real git runner for a collector test, with mailmap
// as the organisation mailmap ("" for none), such as fixture.Mailmap(t).
func newRunner(t testing.TB, mailmap string) domain.GitRunner {
	t.Helper()
	r, err := gitcli.New(t.Context(), gitcli.Config{MailmapFile: mailmap})
	if err != nil {
		t.Fatalf("gitcli.New: %v", err)
	}
	return r
}
