package main

import (
	"reflect"
	"testing"
)

func TestParseFenceUIDs(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    []uint32
		wantErr bool
	}{
		{"1002", []uint32{1002}, false},
		{" 1002 , 1003 ", []uint32{1002, 1003}, false},
		{"", nil, false},
		{"sidecar", nil, true},
		{"-1", nil, true},
	} {
		got, err := parseFenceUIDs(tc.in)
		if (err != nil) != tc.wantErr || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("parseFenceUIDs(%q) = %v, %v", tc.in, got, err)
		}
	}
}
