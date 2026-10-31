package device

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNotFound is returned when a device does not exist.
var ErrNotFound = errors.New("device not found")

// ErrInUse groups every operation rejected because the device is in use.
// Callers should match it with errors.Is.
var ErrInUse = errors.New("device is in use")

// Specific in-use violations. Both wrap ErrInUse.
var (
	ErrNameBrandLocked = fmt.Errorf("%w: name and brand cannot be changed", ErrInUse)
	ErrDeleteInUse     = fmt.Errorf("%w: it cannot be deleted", ErrInUse)
)

// Field error codes, shared with the API contract.
const (
	CodeRequired     = "required"
	CodeTooLong      = "too_long"
	CodeInvalidValue = "invalid_value"
	CodeReadOnly     = "read_only"
)

// FieldError describes why a single input field was rejected.
type FieldError struct {
	Field   string
	Code    string
	Message string
}

// ValidationError lists every invalid field of a request.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		parts[i] = f.Field + " " + f.Message
	}
	return "invalid device: " + strings.Join(parts, "; ")
}

// validationErrors accumulates field errors.
type validationErrors []FieldError

func (v *validationErrors) add(field, code, msg string) {
	*v = append(*v, FieldError{Field: field, Code: code, Message: msg})
}

func (v validationErrors) err() error {
	if len(v) == 0 {
		return nil
	}
	return &ValidationError{Fields: v}
}
