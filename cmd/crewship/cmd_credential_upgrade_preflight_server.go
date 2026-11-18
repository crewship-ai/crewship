//go:build !clionly

package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/buildinfo"
)

func init() {
	inspectCredentialUpgrade = func(ctx context.Context, in credentialUpgradeInspection) (any, error) {
		path, err := filepath.Abs(in.Database)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("retained database must be an existing regular file")
		}
		uri := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
		query := url.Values{"mode": []string{"ro"}}
		uri.RawQuery = query.Encode()
		db, err := sql.Open("sqlite", uri.String())
		if err != nil {
			return nil, err
		}
		defer db.Close()
		db.SetMaxOpenConns(1)
		if _, err := db.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
			return nil, fmt.Errorf("read-only database inspection unavailable")
		}
		report, err := api.InspectCredentialUpgrade(ctx, db, in.Workspace, in.Keeper, in.Sandbox)
		if err != nil {
			return nil, err
		}
		target := buildinfo.Resolve(version, commit, date).Commit
		targetKnown := len(target) >= 7 && len(target) <= 40 && strings.Trim(target, "0123456789abcdef") == ""
		if !in.ConfigurationKnown || !targetKnown {
			report.Ready = false
			if report.State == "PASS" {
				report.State = "UNKNOWN"
			}
		}
		return struct {
			*api.CredentialUpgradeReport `json:",inline" yaml:",inline"`
			SourceBuild                  string `json:"source_build" yaml:"source_build"`
			TargetBuild                  string `json:"target_build" yaml:"target_build"`
			ConfigurationKnown           bool   `json:"configuration_known" yaml:"configuration_known"`
			TargetBuildKnown             bool   `json:"target_build_known" yaml:"target_build_known"`
		}{report, in.SourceBuild, target, in.ConfigurationKnown, targetKnown}, nil
	}
}
