// stop-fixture emits the actual production bootstrap response without a server.
package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"

	"github.com/crewship-ai/crewship/internal/pagebuild"
)

const studio = "https://studio.crewship.test"
const runtime = "https://runtime.artifacts.test"

type response struct {
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"`
}

func bootstrap() response {
	if err := pagebuild.ValidateRuntimeOrigin(runtime, studio); err != nil {
		panic(err)
	}
	writer := httptest.NewRecorder()
	pagebuild.ServeRuntime(writer, studio)
	return response{writer.Code, writer.Header(), writer.Body.String()}
}

func main() {
	if err := json.NewEncoder(os.Stdout).Encode(bootstrap()); err != nil {
		panic(err)
	}
}
