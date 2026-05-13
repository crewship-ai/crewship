package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func checkCredentialsRequired(client doctorHTTPGetter, ws string, def map[string]interface{}) []doctorCheck {
	creds, ok := def["credentials_required"].([]interface{})
	if !ok || len(creds) == 0 {
		// No declared creds is fine — many routines need none.
		return []doctorCheck{{
			Name:    "credentials_required",
			Level:   doctorOK,
			Message: "no credentials declared",
		}}
	}

	available := fetchActiveCredentialTypes(client, ws)
	if available == nil {
		return []doctorCheck{{
			Name:    "credentials_required",
			Level:   doctorWarn,
			Message: "could not fetch workspace credentials — skipping match check",
		}}
	}

	out := make([]doctorCheck, 0, len(creds))
	for _, raw := range creds {
		creq, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		credType, _ := creq["type"].(string)
		credType = strings.ToUpper(credType)
		if credType == "" {
			continue
		}
		if _, found := available[credType]; !found {
			out = append(out, doctorCheck{
				Name:    "credential:" + credType,
				Level:   doctorFail,
				Message: fmt.Sprintf("declared credential type %q has no active match in workspace", credType),
				Hint:    "create one with `crewship credential create --type=" + credType + " ...`",
			})
		} else {
			out = append(out, doctorCheck{
				Name:    "credential:" + credType,
				Level:   doctorOK,
				Message: "active credential of type found",
			})
		}
	}
	return out
}

func fetchActiveCredentialTypes(client doctorHTTPGetter, _ string) map[string]struct{} {
	resp, err := client.Get("/api/v1/credentials")
	if err != nil || resp == nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var rows []struct {
		Provider string `json:"provider"`
		Type     string `json:"type"`
		Status   string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil
	}
	out := map[string]struct{}{}
	for _, r := range rows {
		if r.Status != "ACTIVE" {
			continue
		}
		// Match either provider name (e.g. "ANTHROPIC") or
		// declared `type` field — different routines write the
		// credentials_required.type with different conventions.
		if r.Provider != "" {
			out[strings.ToUpper(r.Provider)] = struct{}{}
		}
		if r.Type != "" {
			out[strings.ToUpper(r.Type)] = struct{}{}
		}
	}
	return out
}
