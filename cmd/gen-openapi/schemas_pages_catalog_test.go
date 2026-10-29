package main

import (
	"reflect"
	"testing"
)

func TestPageCatalogPublishedParametersPreserveExistingPaging(t *testing.T) {
	operations := loadSpecOperations(t)
	got := specQueryParams(t, operations, "GET", "/api/v1/pages")
	want := []string{"access", "archived", "audience", "folder", "kind", "limit", "offset", "owner", "q", "sort", "test_items", "version", "workspace_id"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("published catalog parameters = %v, want %v", got, want)
	}
	for _, parameter := range operations["/api/v1/pages"]["get"].Parameters {
		if parameter.Required {
			t.Fatalf("legacy unfiltered catalog unexpectedly requires %s", parameter.Name)
		}
	}
}
