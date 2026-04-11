package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/mattn/go-isatty"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// Shared lipgloss styles used by Table rendering. Colors respect NO_COLOR
// and non-TTY environments automatically via lipgloss/termenv detection.
var (
	tableHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#FAFAFA")).
				Background(lipgloss.Color("#7D56F4")).
				Padding(0, 1).
				Align(lipgloss.Center)

	tableCellStyle = lipgloss.NewStyle().
			Padding(0, 1)

	tableBorderColor = lipgloss.Color("#626262")
)

// Formatter renders CLI output in the configured format (table, json, yaml, or quiet).
type Formatter struct {
	Format string
	Writer io.Writer
}

// NewFormatter creates a Formatter for the given output format.
func NewFormatter(format string) *Formatter {
	return &Formatter{
		Format: format,
		Writer: os.Stdout,
	}
}

// Table prints data as an aligned table.
// headers: column names
// rows: each row is a slice of strings matching headers length
//
// Uses lipgloss/table for styled TTY output (rounded borders, header
// background, zebra rows). For non-TTY (piped output, NO_COLOR), lipgloss
// automatically strips styling and produces plain-text-friendly output
// suitable for awk/grep piping.
//
// "quiet" format falls back to first-column-only output for scripting.
func (f *Formatter) Table(headers []string, rows [][]string) {
	if f.Format == "quiet" {
		for _, row := range rows {
			if len(row) > 0 {
				fmt.Fprintln(f.Writer, row[0])
			}
		}
		return
	}

	// Empty dataset → print just the headers so users see column names.
	if len(rows) == 0 {
		w := tabwriter.NewWriter(f.Writer, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, Bold+strings.Join(headers, "\t")+Reset)
		w.Flush()
		fmt.Fprintln(f.Writer, Dim+"(no results)"+Reset)
		return
	}

	t := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(tableBorderColor)).
		Headers(headers...).
		Rows(rows...).
		StyleFunc(func(row, _ int) lipgloss.Style {
			// table.HeaderRow (constant = -1) is the header row.
			if row == table.HeaderRow {
				return tableHeaderStyle
			}
			return tableCellStyle
		})

	fmt.Fprintln(f.Writer, t.Render())
}

// Detail prints key-value pairs for a single entity.
func (f *Formatter) Detail(pairs [][]string) {
	w := tabwriter.NewWriter(f.Writer, 0, 0, 2, ' ', 0)
	for _, pair := range pairs {
		if len(pair) >= 2 {
			fmt.Fprintf(w, "%s%s:%s\t%s\n", Bold, pair[0], Reset, pair[1])
		}
	}
	w.Flush()
}

// Markdown renders a markdown string to the formatter's writer.
//
// On a TTY with an interactive format (default/auto), it uses glamour to
// render styled markdown (headings, code blocks, lists, tables). On non-TTY
// or "quiet"/"json"/"yaml" formats, it prints the raw markdown as-is so
// downstream tooling (grep, awk, scripts) still work.
//
// A zero-length input is a no-op. Errors from glamour are non-fatal —
// on any failure, the raw markdown is emitted as a fallback.
func (f *Formatter) Markdown(md string) {
	if md == "" {
		return
	}

	// Non-styled formats: print raw markdown.
	if f.Format == "json" || f.Format == "yaml" || f.Format == "quiet" {
		fmt.Fprintln(f.Writer, md)
		return
	}

	// Determine if we're writing to a real terminal (styled rendering)
	// or a non-TTY sink (pipes, buffers, files). Non-TTY → use "notty" style
	// which produces plain ANSI-free output that still respects word wrap.
	isFileTTY := false
	width := 100
	if file, ok := f.Writer.(*os.File); ok {
		if isatty.IsTerminal(file.Fd()) || isatty.IsCygwinTerminal(file.Fd()) {
			isFileTTY = true
			if w, _, err := term.GetSize(int(file.Fd())); err == nil && w > 20 {
				width = w - 4
				if width > 120 {
					width = 120 // readable line length cap
				}
			}
		}
	}

	style := "notty"
	if isFileTTY {
		style = glamourStyleForEnv()
	}

	renderer, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(style),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		fmt.Fprintln(f.Writer, md)
		return
	}
	rendered, err := renderer.Render(md)
	if err != nil {
		// Fallback: emit raw markdown on any render error.
		fmt.Fprintln(f.Writer, md)
		return
	}
	fmt.Fprint(f.Writer, rendered)
}

// glamourStyleForEnv picks a readable style based on NO_COLOR env variable.
// Uses "dark" as default since most terminals use dark backgrounds.
func glamourStyleForEnv() string {
	if os.Getenv("NO_COLOR") != "" {
		return "notty"
	}
	return "dark"
}

// JSON prints data as indented JSON.
func (f *Formatter) JSON(v interface{}) error {
	enc := json.NewEncoder(f.Writer)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// YAML prints data as YAML.
func (f *Formatter) YAML(v interface{}) error {
	return yaml.NewEncoder(f.Writer).Encode(v)
}

// Auto routes to the correct format based on f.Format.
// tableHeaders/tableRows for table format, v for json/yaml.
func (f *Formatter) Auto(v interface{}, tableHeaders []string, tableRows [][]string) error {
	switch f.Format {
	case "json":
		return f.JSON(v)
	case "yaml":
		return f.YAML(v)
	case "quiet":
		for _, row := range tableRows {
			if len(row) > 0 {
				fmt.Fprintln(f.Writer, row[0])
			}
		}
		return nil
	default:
		f.Table(tableHeaders, tableRows)
		return nil
	}
}

// AutoDetail routes to the correct format for single-entity detail views.
func (f *Formatter) AutoDetail(v interface{}, pairs [][]string) error {
	switch f.Format {
	case "json":
		return f.JSON(v)
	case "yaml":
		return f.YAML(v)
	case "quiet":
		// For quiet mode on detail, print the first value (usually ID)
		if len(pairs) > 0 && len(pairs[0]) >= 2 {
			fmt.Fprintln(f.Writer, pairs[0][1])
		}
		return nil
	default:
		f.Detail(pairs)
		return nil
	}
}

// PrintSuccess prints a green success message.
func PrintSuccess(msg string) {
	fmt.Fprintf(os.Stderr, "%s%s%s\n", Green, msg, Reset)
}

// PrintError prints a red error message.
func PrintError(msg string) {
	fmt.Fprintf(os.Stderr, "%sError: %s%s\n", Red, msg, Reset)
}

// PrintWarning prints a yellow warning message.
func PrintWarning(msg string) {
	fmt.Fprintf(os.Stderr, "%sWarning: %s%s\n", Yellow, msg, Reset)
}
