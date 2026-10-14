// Package report is the report model, stillhere.report/v1, and its text,
// Markdown and JSON renderers (docs/spec/report-json.md). The JSON form is
// the published language for tools that do not read facts.db.
//
// It is part of the functional core: renderers write to an io.Writer and
// never open files, and every slice is sorted before it is rendered, so two
// runs on the same inputs render byte-identical output.
package report

import (
	"fmt"
	"io"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// Schema is the version tag every report carries.
const Schema = "stillhere.report/v1"

// Model is one stillhere.report/v1 document. The full shape lands with #11;
// docs/spec/report-json.md is its draft.
type Model struct {
	Schema      string // always Schema
	ToolVersion string
	Now         time.Time
	Policy      domain.PolicyRef
}

// Text renders m as aligned plain text, readable at 100 columns.
func Text(w io.Writer, m *Model) error {
	return fmt.Errorf("report.Text: %w (#11)", domain.ErrNotImplemented)
}

// Markdown renders m as GitHub-flavoured Markdown.
func Markdown(w io.Writer, m *Model) error {
	return fmt.Errorf("report.Markdown: %w (#11)", domain.ErrNotImplemented)
}

// JSON renders m as stillhere.report/v1 JSON, in a stable order.
func JSON(w io.Writer, m *Model) error {
	return fmt.Errorf("report.JSON: %w (#11)", domain.ErrNotImplemented)
}
