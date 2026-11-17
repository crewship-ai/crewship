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

var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)

func Parse(value string) (Account, error) {
	var a Account
	if len(value) > 32768 || json.Unmarshal([]byte(value), &a) != nil {
		return a, errors.New("Bedrock requires an account document with authentication method, API key, region and models")
	}
	if a.AuthMethod != "bedrock_api_key" {
		return a, errors.New("Bedrock supports the bedrock_api_key authentication method")
	}
	if len(a.APIKey) < 1 || len(a.APIKey) > 16384 || strings.ContainsAny(a.APIKey, "\r\n\t ") {
		return a, errors.New("invalid Bedrock API key")
	}
	if a.Endpoint() == "" {
		return a, errors.New("unsupported AWS Bedrock runtime region")
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
	if a.ExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339, a.ExpiresAt); err != nil {
			return a, errors.New("Bedrock API key expiry must use RFC3339 format when provided")
		}
	}
	return a, nil
}

// Endpoint chooses a fixed commercial inference destination, never a URL built
// from user input. Keep this runtime-only allowlist aligned with AWS:
// https://docs.aws.amazon.com/general/latest/gr/bedrock.html
func (a Account) Endpoint() string {
	switch a.Region {
	case "us-east-1":
		return "https://bedrock-runtime.us-east-1.amazonaws.com"
	case "us-east-2":
		return "https://bedrock-runtime.us-east-2.amazonaws.com"
	case "us-west-2":
		return "https://bedrock-runtime.us-west-2.amazonaws.com"
	case "ap-south-1":
		return "https://bedrock-runtime.ap-south-1.amazonaws.com"
	case "ap-south-2":
		return "https://bedrock-runtime.ap-south-2.amazonaws.com"
	case "ap-northeast-1":
		return "https://bedrock-runtime.ap-northeast-1.amazonaws.com"
	case "ap-northeast-2":
		return "https://bedrock-runtime.ap-northeast-2.amazonaws.com"
	case "ap-northeast-3":
		return "https://bedrock-runtime.ap-northeast-3.amazonaws.com"
	case "ap-southeast-1":
		return "https://bedrock-runtime.ap-southeast-1.amazonaws.com"
	case "ap-southeast-2":
		return "https://bedrock-runtime.ap-southeast-2.amazonaws.com"
	case "ca-central-1":
		return "https://bedrock-runtime.ca-central-1.amazonaws.com"
	case "eu-central-1":
		return "https://bedrock-runtime.eu-central-1.amazonaws.com"
	case "eu-central-2":
		return "https://bedrock-runtime.eu-central-2.amazonaws.com"
	case "eu-west-1":
		return "https://bedrock-runtime.eu-west-1.amazonaws.com"
	case "eu-west-2":
		return "https://bedrock-runtime.eu-west-2.amazonaws.com"
	case "eu-west-3":
		return "https://bedrock-runtime.eu-west-3.amazonaws.com"
	case "eu-south-1":
		return "https://bedrock-runtime.eu-south-1.amazonaws.com"
	case "eu-south-2":
		return "https://bedrock-runtime.eu-south-2.amazonaws.com"
	case "eu-north-1":
		return "https://bedrock-runtime.eu-north-1.amazonaws.com"
	case "sa-east-1":
		return "https://bedrock-runtime.sa-east-1.amazonaws.com"
	default:
		return ""
	}
}
func (a Account) Expired(now time.Time) bool {
	if a.ExpiresAt == "" {
		return false
	}
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
