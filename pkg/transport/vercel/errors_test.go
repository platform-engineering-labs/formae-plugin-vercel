// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package vercel

import (
	"fmt"
	"testing"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func TestClassifyStatus(t *testing.T) {
	tests := []struct {
		status int
		want   resource.OperationErrorCode
	}{
		{400, resource.OperationErrorCodeInvalidRequest},
		{422, resource.OperationErrorCodeInvalidRequest},
		{401, resource.OperationErrorCodeInvalidCredentials},
		{402, resource.OperationErrorCodeAccessDenied},
		{403, resource.OperationErrorCodeAccessDenied},
		{404, resource.OperationErrorCodeNotFound},
		{409, resource.OperationErrorCodeAlreadyExists},
		{429, resource.OperationErrorCodeThrottling},
		{500, resource.OperationErrorCodeServiceInternalError},
		{503, resource.OperationErrorCodeServiceInternalError},
		{418, resource.OperationErrorCodeInternalFailure},
	}
	for _, tt := range tests {
		if got := ClassifyStatus(tt.status); got != tt.want {
			t.Errorf("ClassifyStatus(%d) = %v, want %v", tt.status, got, tt.want)
		}
	}
}

// Vercel reports "this env var already exists" as 403, not 409. Mapping that to
// AccessDenied would make the agent give up instead of reconciling.
func TestClassifyError_403AlreadyExists(t *testing.T) {
	err := &APIError{
		StatusCode: 403,
		Code:       "forbidden",
		Message:    `The environment variable "FOO" already exists`,
	}
	if got := ClassifyError(err); got != resource.OperationErrorCodeAlreadyExists {
		t.Errorf("ClassifyError(403 already exists) = %v, want AlreadyExists", got)
	}
}

func TestClassifyError_403Forbidden(t *testing.T) {
	err := &APIError{StatusCode: 403, Code: "forbidden", Message: "Not authorized"}
	if got := ClassifyError(err); got != resource.OperationErrorCodeAccessDenied {
		t.Errorf("ClassifyError(403 forbidden) = %v, want AccessDenied", got)
	}
}

func TestClassifyError_NonAPIError(t *testing.T) {
	if got := ClassifyError(fmt.Errorf("dial tcp: timeout")); got != resource.OperationErrorCodeInternalFailure {
		t.Errorf("ClassifyError(plain error) = %v, want InternalFailure", got)
	}
}

func TestIsNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"404", &APIError{StatusCode: 404, Code: "not_found"}, true},
		{"wrapped 404", fmt.Errorf("read: %w", &APIError{StatusCode: 404}), true},
		{"403 forbidden", &APIError{StatusCode: 403, Code: "forbidden"}, false},
		{"400 with not_found code", &APIError{StatusCode: 400, Code: "not_found"}, true},
		{"nil", nil, false},
		{"plain error", fmt.Errorf("boom"), false},
	}
	for _, tt := range tests {
		if got := IsNotFound(tt.err); got != tt.want {
			t.Errorf("IsNotFound(%s) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestAPIError_Error(t *testing.T) {
	e := &APIError{StatusCode: 404, Code: "not_found", Message: "Could not find the project"}
	want := `vercel API: HTTP 404 (not_found): Could not find the project`
	if e.Error() != want {
		t.Errorf("Error() = %q, want %q", e.Error(), want)
	}
}
