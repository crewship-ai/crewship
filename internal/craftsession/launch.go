package craftsession

import (
	"context"
	"os"

	"github.com/crewship-ai/crewship/internal/craftengine"
)

// WorkerFlag is the first argument that makes the server binary behave as an
// engine worker (see craftengine.WorkerMain) instead of starting the server.
const WorkerFlag = "--internal-craft-engine-worker"

// ExecLauncher starts the engine by re-executing the server binary. The
// worker gets an empty environment (nothing of the server's secrets or
// configuration), one working directory, the verified module path and the
// module's expected digest; it needs no writable root file system.
func ExecLauncher(application, modulePath, sha256Hex string, memoryMiB int) Launcher {
	return func(_ context.Context, dir string, engineArgs []string) (*craftengine.Worker, error) {
		exe, err := os.Executable()
		if err != nil {
			return nil, craftengine.ErrUnavailable
		}
		return craftengine.StartWorker(craftengine.WorkerSpec{
			Exe: exe, Args: []string{WorkerFlag}, Env: []string{"LANG=C.UTF-8"},
			Application: application, ModulePath: modulePath, SHA256: sha256Hex, Dir: dir,
			MemoryMiB: memoryMiB, MaxLineBytes: 256 << 20, EngineArgs: engineArgs,
		})
	}
}
