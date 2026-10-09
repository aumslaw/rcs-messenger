package rcs

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/zagnu-labs/rcs-messenger/internal/apperr"
	"github.com/zagnu-labs/rcs-messenger/internal/providers/rcsdirect"
)

const (
	CodeDisabled           apperr.Code = "rcs_disabled"
	CodeBadNumber          apperr.Code = "rcs_bad_number"
	CodeEmptyMessage       apperr.Code = "rcs_empty_message"
	CodeNotAllowlisted     apperr.Code = "rcs_not_allowlisted"
	CodeRefused            apperr.Code = "rcs_refused"
	CodeSenderUnavailable  apperr.Code = "rcs_sender_unavailable"
	CodeBlockFailed        apperr.Code = "rcs_block_failed"
	CodeBadNudge           apperr.Code = "rcs_bad_nudge"
	CodeNudgeUnsupported   apperr.Code = "rcs_nudge_unsupported"
	CodeResultsUnavailable apperr.Code = "rcs_results_unavailable"
)

// ReasonKnownUnencrypted is the refusal for a number whose past send went
// out without encryption. It is decided here, from the send results, before
// anything reaches the phone. See Outcome.
const ReasonKnownUnencrypted = "known_unencrypted"

func resultsErr(err error) *apperr.AppError {
	return apperr.Wrap(err, CodeResultsUnavailable, http.StatusServiceUnavailable,
		"rcs send results could not be read, refusing rather than risk a number on record")
}

func badNudgeErr(action string) *apperr.AppError {
	return apperr.New(CodeBadNudge, http.StatusBadRequest,
		fmt.Sprintf("unknown nudge action %q, want one of %s or %s",
			action, strings.Join(rcsdirect.NudgeSteps, ", "), rcsdirect.NudgeAll))
}

func nudgeUnsupportedErr() *apperr.AppError {
	return apperr.New(CodeNudgeUnsupported, http.StatusNotImplemented,
		"the configured sender cannot nudge a conversation; only the in-process phone can")
}

func disabledErr() *apperr.AppError {
	return apperr.New(CodeDisabled, http.StatusServiceUnavailable,
		"rcs sending is disabled or no sender is configured")
}

func badNumberErr(err error) *apperr.AppError {
	return apperr.Wrap(err, CodeBadNumber, http.StatusBadRequest, err.Error())
}

func emptyMessageErr() *apperr.AppError {
	return apperr.New(CodeEmptyMessage, http.StatusBadRequest, "message is required")
}

func notAllowlistedErr(to string) *apperr.AppError {
	return apperr.New(CodeNotAllowlisted, http.StatusBadRequest,
		"number is not on the rcs allowlist: "+to)
}

func refusedErr(err error, reason string, retryable bool) *apperr.AppError {
	msg := "refused, nothing sent: " + reason
	if retryable {
		msg += " (retryable)"
	}
	return apperr.Wrap(err, CodeRefused, http.StatusConflict, msg)
}

func senderErr(err error) *apperr.AppError {
	return apperr.Wrap(err, CodeSenderUnavailable, http.StatusBadGateway,
		"rcs sender could not be reached")
}

func blockFailedErr(err error) *apperr.AppError {
	return apperr.Wrap(err, CodeBlockFailed, http.StatusBadGateway,
		"opt-out could not be recorded with the opt-out store")
}
