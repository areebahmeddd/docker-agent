package v8

// ValidateHooksConfigForTest exposes the unexported [HooksConfig.validate]
// method for use by tests in the v8_test package. It is only compiled
// during `go test`.
func ValidateHooksConfigForTest(h *HooksConfig) error {
	return h.validate()
}
