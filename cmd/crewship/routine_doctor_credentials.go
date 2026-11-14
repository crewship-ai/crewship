package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/pipeline"
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
		// name.<NAME> selects one credential by its exact name.
		if name, byName := pipeline.CredentialRefName(credType); byName {
			if _, found := available["name."+name]; found {
				out = append(out, doctorCheck{Name: "credential:name." + name, Level: doctorOK, Message: "active credential with this name found"})
			} else {
				out = append(out, doctorCheck{
					Name:    "credential:name." + name,
					Level:   doctorFail,
					Message: fmt.Sprintf("declared credential %q has no active credential with that name in workspace", name),
					Hint:    "create it with `crewship credential create " + name + " ...`",
				})
			}
			continue
		}
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
	if len(out) == 0 {
		return []doctorCheck{{
			Name:    "credentials_required",
			Level:   doctorWarn,
			Message: "credentials_required declared but no valid credential types were found",
		}}
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
		Name     string `json:"name" yaml:"name"`
		Provider string `json:"provider" yaml:"provider"`
		Type     string `json:"type" yaml:"type"`
		Status   string `json:"status" yaml:"status"`
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
		// By-name references keep the name's case ("name." cannot collide
		// with an upper-cased type or provider key).
		if r.Name != "" {
			out["name."+r.Name] = struct{}{}
		}
	}
	return out
}
