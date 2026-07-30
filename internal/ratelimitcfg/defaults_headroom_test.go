package ratelimitcfg

import "testing"

// A shipped default that throttles ordinary use is a bug, not a safety margin.
//
// Measured against the real dashboard on a dev instance: selecting one agent in
// the explorer costs 11 API requests (agent detail, avatar, issues, skills,
// credentials, runs, chats, inbox, notification channels, composio binds, and
// the list refresh). A first page load costs ~34. So the old 120 req/min
// General API bucket was spent after roughly ELEVEN CLICKS — a person browsing
// at a normal pace hit a 429, the realtime stream died with it, and the UI
// showed "Reconnecting".
//
// These assertions are deliberately expressed as "how much ordinary use fits",
// not as bare numbers: they fail if someone tightens a default back to
// something a human can reach by using the product.

// Cost of one agent selection in the dashboard, measured 2026-07-29.
const requestsPerClick = 11

// A fast operator clicking roughly twice a second for a full minute, which is
// already well past normal use.
const aggressiveClicksPerMin = 120

func TestGeneralAPIDefaultSurvivesAggressiveClicking(t *testing.T) {
	got := DefaultFor(KeyHTTPAPIPerMin)
	need := requestsPerClick * aggressiveClicksPerMin // 1320

	if got < need {
		t.Errorf("General API default = %d req/min; a person clicking %d times a minute "+
			"spends %d, so they would be throttled while simply using the product",
			got, aggressiveClicksPerMin, need)
	}
}

func TestAuthDefaultSurvivesASharedOfficeIP(t *testing.T) {
	// The auth bucket is per-IP, and a whole office behind one NAT address is
	// one IP. At the old default of 10/min, the eleventh person to sign in
	// after a deploy was locked out of a product they had done nothing wrong in.
	if got := DefaultFor(KeyHTTPAuthPerMin); got < 100 {
		t.Errorf("auth default = %d req/min — too tight for many users behind one NAT IP", got)
	}
}

func TestLockoutIsAWallForAttackersNotForTypos(t *testing.T) {
	if got := DefaultFor(KeyLoginLockoutThresh); got < 25 {
		t.Errorf("lockout threshold = %d; a user who has forgotten which password they used "+
			"should not lock themselves out", got)
	}
	// Still a real lockout, not an off switch.
	if got := DefaultFor(KeyLoginLockoutThresh); got > lockoutThresholdMax {
		t.Errorf("lockout threshold = %d exceeds its own ceiling", got)
	}
	// The duration is deliberately NOT loosened along with everything else.
	// It only applies after the threshold above has been crossed, which an
	// honest user essentially never does, while shortening it would hand a
	// dictionary run proportionally more windows. Room for human error lives
	// in the threshold; this stays a deterrent.
	if got := DefaultFor(KeyLoginLockoutDurSec); got < 300 {
		t.Errorf("lockout duration = %ds; once an account has actually locked, the wait should still cost an attacker something", got)
	}
}

// Credential reveal is the one route that returns a stored secret in plaintext.
// It is raised too — 3/min made looking at four credentials a chore — but it
// stays a distinct, much tighter bucket than the general API on purpose. If
// these two ever converge, the vault has lost a defence.
func TestCredentialRevealStaysTighterThanEverythingElse(t *testing.T) {
	reveal := DefaultFor(KeyHTTPCredRevealPerMin)
	general := DefaultFor(KeyHTTPAPIPerMin)

	if reveal < 30 {
		t.Errorf("reveal default = %d/min — an operator reading a handful of credentials should not wait", reveal)
	}
	if reveal*10 > general {
		t.Errorf("reveal default = %d is not meaningfully tighter than the general bucket (%d); "+
			"the point of a separate bucket is that enumeration hits a wall the UI never does",
			reveal, general)
	}
}

// Every default must sit inside its own declared bounds — a default the admin
// UI would reject as out of range is a broken registry entry.
func TestEveryDefaultIsWithinItsOwnRange(t *testing.T) {
	for _, m := range registry {
		if m.Default < m.Min || m.Default > m.Max {
			t.Errorf("%s: default %d outside [%d, %d]", m.Key, m.Default, m.Min, m.Max)
		}
	}
}
