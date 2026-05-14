package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsSensitiveStaticPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/.env", true},
		{"/.git", true},
		{"/.git/HEAD", true},
		{"/.git/config", true},
		{"/.aws/credentials", true},
		{"/.ssh/id_rsa", true},
		{"/debug/vars", true},
		{"/debug/pprof", true}, // bare form — added per CodeRabbit slash-bypass note
		{"/debug/pprof/", true},
		{"/debug/pprof/heap", true},
		{"/server-status", true},
		{"/package.json", true},
		{"/go.mod", true},
		{"/wp-config.php", true},

		// Legitimate SPA routes — must NOT be flagged.
		{"/", false},
		{"/dashboard", false},
		{"/issues/eng-1", false},
		{"/api/v1/health", false}, // /api/* is handled separately
		{"/exposed/abc123/", false},
		{"/static/main.js", false},
		{"/_next/static/chunks/x.js", false},
		// Paths that look risky but aren't in the denylist.
		{"/dotenv-info", false},
		{"/git-tutorial", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			assert.Equal(t, tc.want, isSensitiveStaticPath(tc.path), tc.path)
		})
	}
}
