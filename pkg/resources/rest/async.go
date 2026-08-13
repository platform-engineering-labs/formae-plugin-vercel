// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package rest

import (
	"context"
	"fmt"

	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// AsyncSpec declares how a resource reports whether it is actually ready.
//
// Some Vercel resources are accepted before they exist: POST
// /v1/connect/networks answers 200 with `status: create_in_progress` and the
// network only becomes usable once that field reads `ready`. Reporting Success
// at accept time makes every dependent resource fail against something that is
// not there yet, so a Definition declaring an AsyncSpec returns InProgress
// instead and lets the agent poll Status().
type AsyncSpec struct {
	// StatusField is the response field carrying the lifecycle state, read
	// after Unwrap, e.g. "status".
	StatusField string

	// Pending are the values meaning "still working" — keep polling.
	Pending []string

	// Failed are the values meaning "this will never become ready" — fail the
	// operation instead of polling until the agent's timeout.
	Failed []string

	// Ready optionally lists the values meaning "done". Leave it empty when the
	// resource only has to name the states it must wait on: anything that is
	// neither Pending nor Failed is then treated as ready. Declaring it makes
	// the check exhaustive — an unlisted value is reported as still in
	// progress rather than silently accepted as success.
	Ready []string
}

// asyncState is the outcome of classifying one observed status value.
type asyncState int

const (
	// asyncReady: the resource is usable.
	asyncReady asyncState = iota
	// asyncPending: keep polling.
	asyncPending
	// asyncFailed: terminal failure.
	asyncFailed
	// asyncUnknown: the field was absent, or its value is not one the
	// definition knows. Never treated as success.
	asyncUnknown
)

// classify reads the status field out of an API response and says what it
// means. It also returns the observed value, for the status message.
func (s *AsyncSpec) classify(raw props) (asyncState, string) {
	value := stringField(raw, s.StatusField)
	switch {
	case containsString(s.Pending, value):
		return asyncPending, value
	case containsString(s.Failed, value):
		return asyncFailed, value
	case containsString(s.Ready, value):
		return asyncReady, value
	case value == "":
		return asyncUnknown, ""
	case len(s.Ready) == 0:
		// No exhaustive ready list: not pending and not failed means ready.
		return asyncReady, value
	default:
		return asyncUnknown, value
	}
}

func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// asyncMessage describes an observed state for a human reading the apply log.
func (s *AsyncSpec) asyncMessage(value string) string {
	if value == "" {
		return fmt.Sprintf("%s is not reported yet", s.StatusField)
	}
	return fmt.Sprintf("%s is %q", s.StatusField, value)
}

// asyncCreateResult converts a create response into a result. Success is
// reported only when the resource says it is ready; anything unproven is
// InProgress, carrying the native id as the RequestID so Status() can re-read
// it.
func (r *Resource) asyncCreateResult(nativeID string, raw props, parent string) *resource.CreateResult {
	spec := r.def.Async
	state, value := spec.classify(raw)
	switch state {
	case asyncFailed:
		return prov.FailCreate(resource.OperationErrorCodeGeneralServiceException,
			fmt.Sprintf("%s creation failed: %s", r.def.Type, spec.asyncMessage(value)))
	case asyncReady:
		return prov.SuccessCreate(nativeID, r.toProperties(raw, parent))
	default:
		return &resource.CreateResult{ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCreate,
			OperationStatus:    resource.OperationStatusInProgress,
			RequestID:          nativeID,
			NativeID:           nativeID,
			ResourceProperties: prov.MustMarshal(r.toProperties(raw, parent)),
			StatusMessage:      spec.asyncMessage(value),
		}}
	}
}

// asyncUpdateResult is asyncCreateResult for an in-place update: the same
// lifecycle field gates readiness after a PATCH.
func (r *Resource) asyncUpdateResult(nativeID string, raw props, parent string) *resource.UpdateResult {
	spec := r.def.Async
	state, value := spec.classify(raw)
	switch state {
	case asyncFailed:
		return prov.FailUpdate(resource.OperationErrorCodeGeneralServiceException,
			fmt.Sprintf("%s update failed: %s", r.def.Type, spec.asyncMessage(value)))
	case asyncReady:
		return prov.SuccessUpdate(nativeID, r.toProperties(raw, parent))
	default:
		return &resource.UpdateResult{ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationUpdate,
			OperationStatus:    resource.OperationStatusInProgress,
			RequestID:          nativeID,
			NativeID:           nativeID,
			ResourceProperties: prov.MustMarshal(r.toProperties(raw, parent)),
			StatusMessage:      spec.asyncMessage(value),
		}}
	}
}

// asyncStatus re-reads the resource and reports where it got to. It is the
// whole of Status() for a definition that declares an AsyncSpec.
func (r *Resource) asyncStatus(ctx context.Context, req *resource.StatusRequest) *resource.StatusResult {
	nativeID := req.NativeID
	if nativeID == "" {
		// Create returned the native id as the RequestID; either identifies
		// the resource.
		nativeID = req.RequestID
	}
	parent, id, err := r.splitNativeID(nativeID)
	if err != nil {
		return prov.FailStatus(resource.OperationErrorCodeInvalidRequest, err.Error())
	}
	raw, err := r.fetch(ctx, parent, id)
	if err != nil {
		return prov.FailStatus(classifyFetchError(err), err.Error())
	}

	spec := r.def.Async
	state, value := spec.classify(raw)
	switch state {
	case asyncFailed:
		return prov.FailStatus(resource.OperationErrorCodeGeneralServiceException,
			fmt.Sprintf("%s: %s", r.def.Type, spec.asyncMessage(value)))
	case asyncReady:
		return &resource.StatusResult{ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          req.RequestID,
			NativeID:           nativeID,
			ResourceProperties: prov.MustMarshal(r.toProperties(raw, parent)),
		}}
	default:
		return &resource.StatusResult{ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationCheckStatus,
			OperationStatus: resource.OperationStatusInProgress,
			RequestID:       req.RequestID,
			NativeID:        nativeID,
			StatusMessage:   spec.asyncMessage(value),
		}}
	}
}
