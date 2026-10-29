package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

// Keep omitted options out of the request: plain page list remains the legacy
// unpaged catalog. Facets repeat on the wire (OR within each, AND across them).
func pageCatalogFilters(cmd *cobra.Command, query url.Values) error {
	if cmd.Flags().Changed("query") {
		value, _ := cmd.Flags().GetString("query")
		query.Set("q", value)
	}
	facets := []struct {
		flag, param string
		allowed     []string
	}{
		{"kind", "kind", []string{"project", "dashboard", "page", "word", "sheet", "slides"}},
		{"access", "access", []string{"private", "open"}},
		{"version-state", "version", []string{"shared", "unshared", "unversioned"}},
		{"owner", "owner", nil},
	}
	for _, facet := range facets {
		if !cmd.Flags().Changed(facet.flag) {
			continue
		}
		values, _ := cmd.Flags().GetStringSlice(facet.flag)
		if len(values) == 0 {
			return fmt.Errorf("%s requires at least one value", facet.flag)
		}
		for _, raw := range values {
			value := strings.TrimSpace(raw)
			valid := false
			if facet.flag == "owner" {
				subjectType, subject, ok := strings.Cut(value, "/")
				valid = value == "me" || (ok && (subjectType == "user" || subjectType == "crew") && subject != "" && !strings.ContainsAny(subject, "/\\ \t\r\n\x00"))
			} else {
				for _, allowed := range facet.allowed {
					if value == allowed {
						valid = true
						break
					}
				}
			}
			if !valid {
				if facet.flag == "owner" {
					return fmt.Errorf("owner must be me, user/<id> or crew/<slug>")
				}
				return fmt.Errorf("%s must be one of %s", facet.flag, strings.Join(facet.allowed, ", "))
			}
			query.Add(facet.param, value)
		}
	}
	if cmd.Flags().Changed("folder") {
		value, _ := cmd.Flags().GetString("folder")
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("folder must be a slug or ~unfiled")
		}
		query.Set("folder", value)
	}
	if cmd.Flags().Changed("sort") {
		value, _ := cmd.Flags().GetString("sort")
		if value != "updated" && value != "name" && value != "kind" {
			return fmt.Errorf("sort must be updated, name or kind")
		}
		query.Set("sort", value)
	}
	return nil
}
