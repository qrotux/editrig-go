package editrig

import "github.com/qrotux/editrig-go/validate"

// HandlerOption configures a set of handlers at construction time.
type HandlerOption func(*Handler)

// UnknownFieldPolicy is validate.UnknownFieldPolicy, re-exported so a caller
// configuring handlers needs only this package.
type UnknownFieldPolicy = validate.UnknownFieldPolicy

const (
	AllowUnknownFields  = validate.AllowUnknownFields
	RejectUnknownFields = validate.RejectUnknownFields
	StripUnknownFields  = validate.StripUnknownFields
)

// BodyLimits bounds whole requests; per-file validation belongs to Entity.Validate.
// Zero values select defaults. Negative values are invalid.
type BodyLimits struct {
	JSONBytes       int64
	MultipartBytes  int64
	MultipartMemory int64
}

func (b BodyLimits) defaults() BodyLimits {
	if b.JSONBytes < 0 || b.MultipartBytes < 0 || b.MultipartMemory < 0 {
		panic("editrig: body limits must not be negative")
	}
	if b.JSONBytes == 0 {
		b.JSONBytes = 4 << 20
	}
	if b.MultipartBytes == 0 {
		b.MultipartBytes = uploadMaxBytes
	}
	if b.MultipartMemory == 0 {
		b.MultipartMemory = multipartMemory
	}
	return b
}

// WithUnknownFields selects the policy for undeclared top-level input keys.
// Nested objects and schema combinators follow the entity's JSON Schema.
func WithUnknownFields(policy UnknownFieldPolicy) HandlerOption {
	switch policy {
	case AllowUnknownFields, RejectUnknownFields, StripUnknownFields:
	default:
		panic("editrig: invalid unknown field policy")
	}
	return func(h *Handler) { h.unknownFields = policy }
}

// WithBodyLimits configures body limits for every write handler in this set.
func WithBodyLimits(limits BodyLimits) HandlerOption {
	limits = limits.defaults()
	return func(h *Handler) { h.bodyLimits = limits }
}
