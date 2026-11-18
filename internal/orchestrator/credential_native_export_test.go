package orchestrator

// Test-only seams for the external-package CLI/API/Vault acceptance. These
// symbols do not exist in production builds or expose a new product API.
var NativeCredentialScriptForTest = buildCredFileScript
var NativeCredentialEnvForTest = appendCredentialFileEnv
var NativeSSHFixtureForTest = startNativeSSHFixture

var NativeCredentialWriteForTest = writeCredentialFiles
