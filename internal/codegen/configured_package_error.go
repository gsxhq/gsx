package codegen

// ConfiguredPackageError reports that a package named by the project
// configuration (a filter package, a named filter, a renderer, a class merger)
// could not be resolved from the module being generated. Where frames the
// configuration entry that named it; Reason is the load-level failure; Err is
// the underlying cause when there is one.
//
// It is a distinct type so a caller that knows WHICH gsx.toml supplied the
// entry can attribute the failure to that file — a nested module inheriting an
// outer module's config is the shape where the package is typically not
// importable at all.
type ConfiguredPackageError struct {
	Where  string
	Path   string
	Reason string
	Err    error
}

func (e *ConfiguredPackageError) Error() string {
	msg := "codegen: " + e.Where + " " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *ConfiguredPackageError) Unwrap() error { return e.Err }
