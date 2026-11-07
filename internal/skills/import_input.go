package skills

import "errors"

// ErrImportInput marks a bulk import request the importer refused as input,
// before any clone: the git URL, its host or port, or the git ref. The API
// answers it 400 (#5097). Failures of the server's own Git, DNS, the
// administrator policy and the clone itself are not input.
var ErrImportInput = errors.New("skills: import input rejected")

type importInputError struct{ err error }

func (e importInputError) Error() string   { return e.err.Error() }
func (e importInputError) Unwrap() []error { return []error{e.err, ErrImportInput} }

// inputError marks err as a rejection of the request itself. The message is
// unchanged.
func inputError(err error) error {
	if err == nil {
		return nil
	}
	return importInputError{err: err}
}
