package reporter

import (
	"embed"
	"io"
	"text/template"

	"github.com/ramplio/ramplio/internal/metrics"
)

//go:embed templates/report.html
var templateFS embed.FS

var reportTmpl = template.Must(
	template.ParseFS(templateFS, "templates/report.html"),
)

// WriteHTML renders a Summary as a self-contained HTML report.
func WriteHTML(w io.Writer, sum metrics.Summary) error {
	return WriteHTMLFromReport(w, SummaryToReport(sum))
}

// WriteHTMLFromReport renders a pre-built Report as HTML (used by the report command).
func WriteHTMLFromReport(w io.Writer, r Report) error {
	return reportTmpl.Execute(w, r)
}
