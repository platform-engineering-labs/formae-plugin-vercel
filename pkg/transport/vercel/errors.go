// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package vercel

import (
	"errors"
	"fmt"
	"strings"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// APIError represents a non-2xx response from the Vercel REST API.
//
// Vercel wraps every error in {"error": {"code": ..., "message": ...}}; Code and
// Message are decoded from that envelope, Body keeps the raw payload for
// diagnostics when the envelope is missing.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	Body       string
}

func (e *APIError) Error() string {
	detail := e.Message
	if detail == "" {
		detail = e.Body
	}
	if e.Code != "" {
		return fmt.Sprintf("vercel API: HTTP %d (%s): %s", e.StatusCode, e.Code, detail)
	}
	return fmt.Sprintf("vercel API: HTTP %d: %s", e.StatusCode, detail)
}

// IsNotFound reports whether err represents a missing resource. Delete treats it
// as success and Read reports NotFound so the agent converges after out-of-band
// deletions.
func IsNotFound(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode == 404 || apiErr.Code == "not_found"
}

// isAlreadyExists reports whether a 403/409 is really a uniqueness conflict.
// Vercel returns 403 both for "not authorized" and for "the environment variable
// cannot be created because it already exists".
func isAlreadyExists(e *APIError) bool {
	msg := strings.ToLower(e.Message)
	return strings.Contains(msg, "already exists") || e.Code == "conflict"
}

// ClassifyStatus maps an HTTP status to a formae operation error code.
func ClassifyStatus(status int) resource.OperationErrorCode {
	switch {
	case status == 400, status == 422:
		return resource.OperationErrorCodeInvalidRequest
	case status == 401:
		return resource.OperationErrorCodeInvalidCredentials
	case status == 402, status == 403:
		return resource.OperationErrorCodeAccessDenied
	case status == 404:
		return resource.OperationErrorCodeNotFound
	case status == 409:
		return resource.OperationErrorCodeAlreadyExists
	case status == 429:
		return resource.OperationErrorCodeThrottling
	case status >= 500 && status <= 599:
		return resource.OperationErrorCodeServiceInternalError
	default:
		return resource.OperationErrorCodeInternalFailure
	}
}

// ClassifyError maps an error returned by Client.Do to a formae operation error
// code, applying the message-shape overrides Vercel's status codes need.
func ClassifyError(err error) resource.OperationErrorCode {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return resource.OperationErrorCodeInternalFailure
	}
	if apiErr.StatusCode == 403 && isAlreadyExists(apiErr) {
		return resource.OperationErrorCodeAlreadyExists
	}
	if IsNotFound(err) {
		return resource.OperationErrorCodeNotFound
	}
	return ClassifyStatus(apiErr.StatusCode)
}
