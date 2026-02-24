package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"gopkg.in/yaml.v3"
)

type Formatter struct {
	Format string
	Writer io.Writer
}

func NewFormatter(format string) *Formatter {
	return &Formatter{
		Format: format,
		Writer: os.Stdout,
	}
}

// Table prints data as an aligned table.
// headers: column names
// rows: each row is a slice of strings matching headers length
func (f *Formatter) Table(headers []string, rows [][]string) {
	if f.Format == "quiet" {
		for _, row := range rows {
			if len(row) > 0 {
				fmt.Fprintln(f.Writer, row[0])
			}
		}
		return
	}

	w := tabwriter.NewWriter(f.Writer, 0, 0, 2, ' ', 0)

	// Header
	coloredHeaders := make([]string, len(headers))
	for i, h := range headers {
		coloredHeaders[i] = Bold + h + Reset
	}
	fmt.Fprintln(w, strings.Join(coloredHeaders, "\t"))

	// Rows
	for _, row := range rows {
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	w.Flush()
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
