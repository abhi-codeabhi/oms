// Package domain holds the pure settings model and rules: a Definition declares a
// configurable key (type, default, scope ceiling, validation, who-can-edit); an
// Override stores a value at an owner/brand/restaurant scope; resolution picks the
// effective value by precedence restaurant > brand > owner > default.
//
// It imports no infrastructure (no pgx/connect/nats). Values are carried as a
// canonical string `Raw` plus a `Type`; parsing/validation lives here so both the
// app layer and tests share one source of truth.
package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Domain errors. Adapters map these to transport codes (see pkg/errors).
var (
	// ErrInvalid is returned for malformed input (blank key, bad value, unknown
	// definition, validation failure, type-parse failure).
	ErrInvalid = errors.New("settings: invalid argument")
	// ErrNotFound is returned when a key has no definition.
	ErrNotFound = errors.New("settings: definition not found")
	// ErrScopeTooDeep is returned when an override targets a scope deeper than the
	// definition's max_scope allows.
	ErrScopeTooDeep = errors.New("settings: scope deeper than definition allows")
	// ErrNotEditable is returned when the caller's role may not edit the key.
	ErrNotEditable = errors.New("settings: caller role may not edit this setting")
)

// ValueType is the canonical type of a setting value. Mirrors the proto enum.
type ValueType int

// Value types. The zero value is Unspecified.
const (
	TypeUnspecified ValueType = iota
	TypeInt
	TypeBool
	TypeString
	TypeDecimal
	TypeJSON
	TypeEnum
)

// Scope is the level an override is stored at / resolved from. Deeper scopes win.
// Mirrors the proto enum; the integer order encodes precedence (higher = deeper).
type Scope int

// Scopes. ScopeUnspecified is the zero value; ScopeDefinition marks "resolved from
// the definition default" (it is not a storable override scope).
const (
	ScopeUnspecified Scope = iota
	ScopeOwner
	ScopeBrand
	ScopeRestaurant
	// ScopeDefinition is a synthetic source meaning "no override, fell back to the
	// definition default". Never persisted.
	ScopeDefinition Scope = 99
)

// Value is a typed setting value carried as a canonical string.
type Value struct {
	Type ValueType
	Raw  string
}

// Definition declares a configurable key: its type, default, the deepest scope it
// may be overridden at, enum options, a validation expression, and who may edit
// it. Definitions are global (not tenant-scoped) and self-registered by services.
type Definition struct {
	Key          string // dotted namespace, e.g. "billing.gst_pct"
	Title        string
	Description  string
	Type         ValueType
	Default      Value
	MaxScope     Scope
	EnumOptions  []string
	Validation   string // e.g. "min:0,max:100"
	EditableBy   string // "platform_admin" | "owner" | "manager"
	FeatureGated bool
}

// Override is a value set for a key at a specific tenant scope.
type Override struct {
	Key          string
	OwnerID      string
	BrandID      string // set when Scope == ScopeBrand or deeper
	RestaurantID string // set when Scope == ScopeRestaurant
	Scope        Scope
	Value        Value
}

// SettingValue is an effective resolved value plus where it came from.
type SettingValue struct {
	Key         string
	Value       Value
	SourceScope Scope
}

// Namespace returns the dotted prefix of the key up to (not including) the last
// segment, e.g. "billing.gst_pct" -> "billing". Single-segment keys return "".
func Namespace(key string) string {
	i := strings.LastIndex(key, ".")
	if i < 0 {
		return ""
	}
	return key[:i]
}

// InNamespace reports whether key belongs to namespace ns. An empty ns matches
// everything (used by "list all"). Matching is prefix-on-a-dot-boundary so
// "billing" matches "billing.gst_pct" but not "billings.x".
func InNamespace(key, ns string) bool {
	if ns == "" {
		return true
	}
	return key == ns || strings.HasPrefix(key, ns+".")
}

// Validate checks a Definition is well-formed before persistence.
func (d Definition) Validate() error {
	if strings.TrimSpace(d.Key) == "" {
		return fmt.Errorf("%w: key is required", ErrInvalid)
	}
	if d.Type == TypeUnspecified {
		return fmt.Errorf("%w: type is required for %q", ErrInvalid, d.Key)
	}
	if d.MaxScope == ScopeUnspecified {
		return fmt.Errorf("%w: max_scope is required for %q", ErrInvalid, d.Key)
	}
	if d.Type == TypeEnum && len(d.EnumOptions) == 0 {
		return fmt.Errorf("%w: enum %q needs enum_options", ErrInvalid, d.Key)
	}
	// The default itself must pass type + validation rules.
	if err := d.Check(d.Default); err != nil {
		return fmt.Errorf("%w: default for %q is invalid: %v", ErrInvalid, d.Key, err)
	}
	return nil
}

// AllowsScope reports whether the definition permits an override at scope s.
// Deeper-than-max is rejected; the definition default scope is never an override.
func (d Definition) AllowsScope(s Scope) bool {
	if s == ScopeUnspecified || s == ScopeDefinition {
		return false
	}
	return s <= d.MaxScope
}

// EditableByRole reports whether a role name may edit this setting. The mapping is
// a simple seniority ladder: platform_admin ⊇ owner ⊇ manager. A definition that
// is editable_by "manager" is also editable by owner and platform_admin.
func (d Definition) EditableByRole(role string) bool {
	rank := map[string]int{
		"platform_admin": 3,
		"owner":          2,
		"brand_admin":    2,
		"manager":        1,
	}
	need, ok := rank[strings.ToLower(strings.TrimSpace(d.EditableBy))]
	if !ok {
		// Unknown editable_by → most restrictive (platform_admin only).
		need = 3
	}
	have := rank[strings.ToLower(strings.TrimSpace(role))]
	return have >= need
}

// Check validates a value against the definition's type, enum membership and
// validation expression (min:/max:). It returns a wrapped ErrInvalid on failure.
func (d Definition) Check(v Value) error {
	if v.Type != TypeUnspecified && v.Type != d.Type {
		return fmt.Errorf("%w: %q expects type %v, got %v", ErrInvalid, d.Key, d.Type, v.Type)
	}
	switch d.Type {
	case TypeInt:
		n, err := strconv.ParseInt(strings.TrimSpace(v.Raw), 10, 64)
		if err != nil {
			return fmt.Errorf("%w: %q is not an integer: %q", ErrInvalid, d.Key, v.Raw)
		}
		return d.checkBounds(float64(n))
	case TypeDecimal:
		f, err := strconv.ParseFloat(strings.TrimSpace(v.Raw), 64)
		if err != nil {
			return fmt.Errorf("%w: %q is not a decimal: %q", ErrInvalid, d.Key, v.Raw)
		}
		return d.checkBounds(f)
	case TypeBool:
		if _, err := strconv.ParseBool(strings.TrimSpace(v.Raw)); err != nil {
			return fmt.Errorf("%w: %q is not a bool: %q", ErrInvalid, d.Key, v.Raw)
		}
		return nil
	case TypeEnum:
		for _, opt := range d.EnumOptions {
			if v.Raw == opt {
				return nil
			}
		}
		return fmt.Errorf("%w: %q must be one of %v, got %q", ErrInvalid, d.Key, d.EnumOptions, v.Raw)
	case TypeString:
		return d.checkLen(len(v.Raw))
	case TypeJSON:
		if strings.TrimSpace(v.Raw) == "" {
			return fmt.Errorf("%w: %q json value is empty", ErrInvalid, d.Key)
		}
		return nil
	default:
		return fmt.Errorf("%w: %q has unsupported type %v", ErrInvalid, d.Key, d.Type)
	}
}

// checkBounds enforces min:/max: from the validation expression on a number.
func (d Definition) checkBounds(n float64) error {
	for k, val := range parseValidation(d.Validation) {
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			continue
		}
		switch k {
		case "min":
			if n < f {
				return fmt.Errorf("%w: %q must be >= %s, got %v", ErrInvalid, d.Key, val, n)
			}
		case "max":
			if n > f {
				return fmt.Errorf("%w: %q must be <= %s, got %v", ErrInvalid, d.Key, val, n)
			}
		}
	}
	return nil
}

// checkLen enforces min:/max: as string-length bounds when present.
func (d Definition) checkLen(n int) error {
	for k, val := range parseValidation(d.Validation) {
		f, err := strconv.Atoi(val)
		if err != nil {
			continue
		}
		switch k {
		case "min":
			if n < f {
				return fmt.Errorf("%w: %q length must be >= %d", ErrInvalid, d.Key, f)
			}
		case "max":
			if n > f {
				return fmt.Errorf("%w: %q length must be <= %d", ErrInvalid, d.Key, f)
			}
		}
	}
	return nil
}

// parseValidation turns "min:0,max:100" into {"min":"0","max":"100"}. Unknown or
// malformed pairs are skipped. An empty expression yields an empty map.
func parseValidation(expr string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(expr, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, ":", 2)
		if len(kv) != 2 {
			continue
		}
		out[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
	}
	return out
}

// Resolve picks the effective value for a definition given the overrides that
// apply to a requested tenant scope. Precedence is restaurant > brand > owner >
// definition default. `overrides` may contain any subset; the deepest applicable
// one wins. The returned SettingValue records the source scope.
func Resolve(def Definition, overrides []Override) SettingValue {
	best := SettingValue{
		Key:         def.Key,
		Value:       def.Default,
		SourceScope: ScopeDefinition,
	}
	bestScope := Scope(-1)
	for _, o := range overrides {
		if o.Key != def.Key {
			continue
		}
		if o.Scope > bestScope {
			bestScope = o.Scope
			best = SettingValue{Key: def.Key, Value: o.Value, SourceScope: o.Scope}
		}
	}
	return best
}
