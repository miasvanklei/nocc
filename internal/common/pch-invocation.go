package common

// PCHInvocation represents the data structure for precompiled header invocation.
type PCHInvocation struct {
	Hash       string   `json:"hash"`
	Compiler   string   `json:"compiler"`
	InputFile  string   `json:"inputFile"`
	OutputFile string   `json:"outputFile"`
	Args       []string `json:"args"`
}
