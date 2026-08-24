// Package release is SafeLane's rejection vocabulary.
//
// One error type, seven categories, and a rule: a rejection says what is
// wrong, where, and what to change. The Remedy field is the whole reason this
// exists rather than fmt.Errorf - an agent reading a failure needs to know what
// to do next, and prose it has to interpret is prose it will interpret wrongly.
//
// The categories are branchable so a caller can tell an operational failure
// from a caller's mistake without parsing text. [CategoryEvidenceUnknown] in
// particular is not a convenience: "GitHub was unreachable" and "the check
// failed" lead to different actions, and collapsing them would make one of
// them stop a release for a reason that is not true.
package release

import (
	"errors"
	"fmt"
	"strings"
)

// ErrorCategory is the coarse, machine-branchable class of a SafeLane rejection.
//
// Callers (the CLI, and later an HTTP or MCP adapter) branch on the category;
// they read [Error.Code] when they need the specific reason and [Error.Remedy]
// when they need to tell an agent what to change.
type ErrorCategory string

const (
	// CategoryMalformedRequest covers input that is not a decodable Release Request
	// at all: invalid JSON, wrong schema version, wrong top-level type.
	CategoryMalformedRequest ErrorCategory = "malformed_request"

	// CategoryForbiddenField covers a request that carried something a caller is
	// never allowed to supply: Kubernetes objects, YAML/JSON patches, template
	// selection, or policy selection. These are rejected, never ignored.
	CategoryForbiddenField ErrorCategory = "forbidden_field"

	// CategoryInvalidRequest covers a well-formed request whose values are
	// unusable: missing target identity, a mutable image reference, a malformed
	// commit SHA.
	CategoryInvalidRequest ErrorCategory = "invalid_request"

	// CategoryEvidenceMissing covers required evidence that does not exist: no
	// check run for the merge commit, no such pull request.
	CategoryEvidenceMissing ErrorCategory = "evidence_missing"

	// CategoryEvidenceFailed covers required evidence that exists and is negative:
	// the check run failed, the pull request is not merged, or the digest resolves
	// to a different repository.
	CategoryEvidenceFailed ErrorCategory = "evidence_failed"

	// CategoryEvidenceUnknown covers evidence SafeLane could not determine:
	// GitHub or GHCR was unreachable, timed out, or answered unintelligibly.
	//
	// This category exists so that an operational failure has somewhere to land
	// that is not "pass" and not "low risk". It must never be collapsed into
	// CategoryEvidenceMissing, and it must never be dropped.
	CategoryEvidenceUnknown ErrorCategory = "evidence_unknown"

	// CategoryInternal covers SafeLane defects. It is never a caller's fault and
	// never authorizes anything.
	CategoryInternal ErrorCategory = "internal"
)

// categorySentinel makes each category usable as an errors.Is target.
type categorySentinel struct{ category ErrorCategory }

func (s categorySentinel) Error() string { return string(s.category) }

// Category sentinels. Use with errors.Is:
//
//	if errors.Is(err, release.ErrForbiddenField) { ... }
var (
	ErrMalformedRequest error = categorySentinel{CategoryMalformedRequest}
	ErrForbiddenField   error = categorySentinel{CategoryForbiddenField}
	ErrInvalidRequest   error = categorySentinel{CategoryInvalidRequest}
	ErrEvidenceMissing  error = categorySentinel{CategoryEvidenceMissing}
	ErrEvidenceFailed   error = categorySentinel{CategoryEvidenceFailed}
	ErrEvidenceUnknown  error = categorySentinel{CategoryEvidenceUnknown}
	ErrInternal         error = categorySentinel{CategoryInternal}
)

// Error is SafeLane's typed, actionable rejection. It is the only error type this
// package returns, and it is JSON-serializable so the same value can be printed by
// the CLI, embedded in a typed decision (#50) or rendered into proof (#52).
type Error struct {
	Category ErrorCategory `json:"category"`
	// Code is a stable machine identifier, e.g. "mutable_image_reference".
	Code string `json:"code"`
	// Field is the JSON path in the Release Request that caused the rejection,
	// e.g. "artifact.image_reference". Empty when the rejection is not field-scoped.
	Field string `json:"field,omitempty"`
	// Message states what is wrong.
	Message string `json:"message"`
	// Remedy states what the caller must change. An agent should be able to act on
	// this without interpreting prose from anywhere else.
	Remedy string `json:"remedy,omitempty"`

	cause error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Category))
	b.WriteString(": ")
	b.WriteString(e.Code)
	if e.Field != "" {
		b.WriteString(" [")
		b.WriteString(e.Field)
		b.WriteString("]")
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	if e.cause != nil {
		b.WriteString(": ")
		b.WriteString(e.cause.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.cause }

// Is reports whether the error matches a category sentinel.
func (e *Error) Is(target error) bool {
	s, ok := target.(categorySentinel)
	return ok && s.category == e.Category
}

// WithCause attaches an underlying error without changing the typed surface.
func (e *Error) WithCause(cause error) *Error {
	clone := *e
	clone.cause = cause
	return &clone
}

func newError(category ErrorCategory, code, field, message, remedy string) *Error {
	return &Error{Category: category, Code: code, Field: field, Message: message, Remedy: remedy}
}

// Invalid builds a CategoryInvalidRequest rejection.
func Invalid(code, field, message, remedy string) *Error {
	return newError(CategoryInvalidRequest, code, field, message, remedy)
}

// Malformed builds a CategoryMalformedRequest rejection.
func Malformed(code, field, message, remedy string) *Error {
	return newError(CategoryMalformedRequest, code, field, message, remedy)
}

// Forbidden builds a CategoryForbiddenField rejection. Intake uses this when a
// request carried Kubernetes configuration, a patch, a template selection, a
// policy selection, its own evidence, its own risk, or its own lane.
//
// The code is "unknown_field" and not "forbidden_field_present" because that
// is the whole point: from the caller's side there is no such field. A
// request carrying "lane" is not asking for something it is not allowed to
// have, it is naming something that does not exist in the Release Request
// schema. Appendix C4 has one schema-level code for exactly this reason.
func Forbidden(field, message string) *Error {
	return newError(CategoryForbiddenField, "unknown_field", field, message,
		"send repository and pull_request only")
}

// MissingEvidenceError builds a CategoryEvidenceMissing rejection.
func MissingEvidenceError(code, field, message, remedy string) *Error {
	return newError(CategoryEvidenceMissing, code, field, message, remedy)
}

// FailedEvidenceError builds a CategoryEvidenceFailed rejection.
func FailedEvidenceError(code, field, message, remedy string) *Error {
	return newError(CategoryEvidenceFailed, code, field, message, remedy)
}

// UnknownEvidenceError builds a CategoryEvidenceUnknown rejection. Use this, and
// never a "missing" or a low-severity result, when SafeLane could not determine the
// answer.
func UnknownEvidenceError(code, field, message, remedy string) *Error {
	return newError(CategoryEvidenceUnknown, code, field, message, remedy)
}

// Internal builds a CategoryInternal error.
func Internal(code, message string) *Error {
	return newError(CategoryInternal, code, "", message, "")
}

// Errors is a set of rejections reported together, so an agent can correct every
// problem in one pass instead of one per round trip.
type Errors []*Error

func (es Errors) Error() string {
	parts := make([]string, 0, len(es))
	for _, e := range es {
		parts = append(parts, e.Error())
	}
	return fmt.Sprintf("%d rejection(s): %s", len(es), strings.Join(parts, "; "))
}

// Unwrap exposes the members to errors.Is and errors.As.
func (es Errors) Unwrap() []error {
	out := make([]error, 0, len(es))
	for _, e := range es {
		out = append(out, e)
	}
	return out
}

// OrNil returns nil when the set is empty, so callers can `return errs.OrNil()`.
func (es Errors) OrNil() error {
	if len(es) == 0 {
		return nil
	}
	return es
}

// Flatten normalizes a single *Error, an Errors set, or any other error into
// an Errors slice. Callers outside this package (intake, in particular) use
// this instead of hand-rolling the same type switch, since a plain error
// from outside the package's constructors still needs to become one
// reportable rejection rather than being dropped.
func Flatten(err error) Errors {
	switch e := err.(type) {
	case nil:
		return nil
	case Errors:
		return e
	case *Error:
		return Errors{e}
	default:
		return Errors{Internal("unclassified_error", err.Error())}
	}
}

// Categorize reports the most severe category present in err, or "" when err
// carries no SafeLane category. Severity order is intentional: unknown outranks
// missing and failed, because "we could not tell" must never be reported as the
// milder, more specific outcome.
func Categorize(err error) ErrorCategory {
	if err == nil {
		return ""
	}
	order := []ErrorCategory{
		CategoryInternal,
		CategoryEvidenceUnknown,
		CategoryForbiddenField,
		CategoryEvidenceFailed,
		CategoryEvidenceMissing,
		CategoryMalformedRequest,
		CategoryInvalidRequest,
	}
	for _, c := range order {
		if errors.Is(err, categorySentinel{c}) {
			return c
		}
	}
	return ""
}
