### Fixed

- Confirm explicit chat cancellation when protected output records `execution_cancelled` with the exact execution's verified process-tree stop receipt, instead of timing out and leaving recovery to report a failed run. Missing or unrelated stop receipts remain unconfirmed.
