// Package bedrock defines Crewship's Bedrock account contract. The complete
// document is encrypted as one credential value; it is never agent environment.
package bedrock

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

const Provider = "AWS_BEDROCK"
const NativeProvider = "amazon-bedrock"
const Slot = "AWS_BEARER_TOKEN_BEDROCK"
const AccountHeader = "X-Crewship-Bedrock-Account"

type Account struct {
	AuthMethod string   `json:"auth_method"`
	APIKey     string   `json:"api_key"`
	Region     string   `json:"region"`
	Models     []string `json:"models"`
	ExpiresAt  string   `json:"expires_at"`
	EUOnly     bool     `json:"eu_only"`
}

var regionPattern = regexp.MustCompile(`^(us|eu|ap|ca|sa|me|af|il|mx)-(north|south|east|west|central|northeast|northwest|southeast|southwest)-[1-9][0-9]?$`)
var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)

func Parse(value string) (Account, error) {
	var a Account
	if len(value) > 32768 || json.Unmarshal([]byte(value), &a) != nil {
		return a, errors.New("Bedrock requires an account document with authentication method, API key, region, models and expiry")
	}
	if a.AuthMethod != "bedrock_api_key" {
		return a, errors.New("Bedrock supports the bedrock_api_key authentication method")
	}
	if len(a.APIKey) < 1 || len(a.APIKey) > 16384 || strings.ContainsAny(a.APIKey, "\r\n\t ") {
		return a, errors.New("invalid Bedrock API key")
	}
	if !regionPattern.MatchString(a.Region) {
		return a, errors.New("unsupported AWS commercial region")
	}
	if len(a.Models) == 0 || len(a.Models) > 32 {
		return a, errors.New("specify between 1 and 32 permitted Bedrock model IDs")
	}
	seen := map[string]bool{}
	for _, m := range a.Models {
		if !modelPattern.MatchString(m) || seen[m] {
			return a, errors.New("Bedrock models must be unique native IDs without a provider prefix")
		}
		if a.EUOnly && (!strings.HasPrefix(a.Region, "eu-") || !strings.HasPrefix(m, "eu.")) {
			return a, errors.New("EU-only Bedrock accounts require an EU region and explicit eu. model IDs")
		}
		seen[m] = true
	}
	if _, err := time.Parse(time.RFC3339, a.ExpiresAt); err != nil {
		return a, errors.New("Bedrock API key expiry is required in RFC3339 format")
	}
	return a, nil
}

func (a Account) Endpoint() string { return "https://bedrock-runtime." + a.Region + ".amazonaws.com" }
func (a Account) Expired(now time.Time) bool {
	t, e := time.Parse(time.RFC3339, a.ExpiresAt)
	return e != nil || !now.Before(t)
}
func (a Account) Allows(model string) bool {
	for _, m := range a.Models {
		if m == model {
			return true
		}
	}
	return false
}
