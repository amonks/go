package llm

// paramsWithUnexported is a test type for TestGenerateSchema_SkipUnexported.
// The private field intentionally has no json tag to avoid go vet warnings,
// but the test still verifies that unexported fields are skipped by
// GenerateSchema regardless of json tags.
type paramsWithUnexported struct {
	Public  string `json:"public"`
	private string //lint:ignore U1000 unexported field for testing
}
