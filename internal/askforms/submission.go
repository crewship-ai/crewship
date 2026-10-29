package askforms

// SubmissionError is an expected rejection before an ask answer is persisted.
// Transports return it only to the sender; it is not a model-run failure.
// It carries field errors from ValidateAnswers, never the submitted answers.
type SubmissionError struct {
	SubmissionID string
	Errors       []AnswerError
	Message      string
}

func (e *SubmissionError) Error() string {
	if len(e.Errors) > 0 {
		return e.Errors[0].Message
	}
	return e.Message
}
