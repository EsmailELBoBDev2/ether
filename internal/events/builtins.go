package events

var builtinRegistrars []func(*Registry)

func registerBuiltin(fn func(*Registry)) {
	builtinRegistrars = append(builtinRegistrars, fn)
}

// RegisterBuiltins registers every built-in event schema with r.
func RegisterBuiltins(r *Registry) {
	for _, fn := range builtinRegistrars {
		fn(r)
	}
}

// NewBuiltinRegistry returns a Registry populated with all built-in event schemas.
func NewBuiltinRegistry() *Registry {
	r := NewRegistry()
	RegisterBuiltins(r)
	return r
}
