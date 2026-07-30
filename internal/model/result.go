// Package model defines the stable wire types shared by the broker and MCP
// frontend.
package model

import "time"

// Result is returned by every ZCR521 tool. Fields intentionally do not use
// omitempty: clients can rely on one stable response shape.
type Result struct {
	Success        bool           `json:"success"`
	Code           string         `json:"code"`
	Message        string         `json:"message"`
	Data           any            `json:"data"`
	Error          *Error         `json:"error"`
	Stdout         string         `json:"stdout"`
	Stderr         string         `json:"stderr"`
	ExitCode       int            `json:"exitCode"`
	DurationMS     int64          `json:"durationMs"`
	TaskID         string         `json:"taskId"`
	RebootRequired bool           `json:"rebootRequired"`
	Artifacts      []Artifact     `json:"artifacts"`
	Strategy       string         `json:"strategy"`
	Meta           map[string]any `json:"-"`
}

type Error struct {
	Type    string         `json:"type"`
	Details string         `json:"details"`
	Fields  map[string]any `json:"fields"`
}

type Artifact struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	URI       string    `json:"uri"`
	MediaType string    `json:"mediaType"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func Success(code, message string, data any) Result {
	return Result{
		Success:   true,
		Code:      code,
		Message:   message,
		Data:      data,
		ExitCode:  0,
		Artifacts: []Artifact{},
		Strategy:  "native",
	}
}

func Failure(code, message, errorType, details string) Result {
	return Result{
		Success:   false,
		Code:      code,
		Message:   message,
		Error:     &Error{Type: errorType, Details: details, Fields: map[string]any{}},
		ExitCode:  -1,
		Artifacts: []Artifact{},
		Strategy:  "none",
	}
}
