### Fixed

- Ollama streaming now honors the redirect policy supplied to its HTTP client,
  matching non-streaming requests while retaining the stream's unlimited total
  timeout and existing transport behavior.
