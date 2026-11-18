//go:build integration

package orchestrator

// Test-only bridge: external-package fixtures exercise the real API without an
// import cycle and feed its production resolver result into the real CLI tools.
var NativeCLIShellWithVaultForTest = testNativeCLIShellEnvironment
