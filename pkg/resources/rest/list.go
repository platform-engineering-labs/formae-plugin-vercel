// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package rest

import (
	"context"
	"fmt"
	"time"

	vercelapi "github.com/platform-engineering-labs/formae-plugin-vercel/pkg/transport/vercel"
)

// Reading a collection is the one place this plugin retries on its own.
//
// Every write goes through the agent's operator, which has its own retry loop
// and its own view of whether repeating a call is safe. Discovery does not:
// List is called straight from the scan loop, so a throttle or a 502 arrives
// here with nobody above to try again. Answering "no resources" in that moment
// is worse than failing, because an empty list is a valid answer that formae
// acts on — it is how managed resources come to look deleted.
//
// The budget is deliberately short. A List call runs inside an RPC the agent
// watches, so the loop has to give up well before that watchdog does; three
// retries over ~7s absorbs the brief 429s Vercel returns for burst traffic
// without holding the scan open.
const (
	listRetryAttempts = 4
	listRetryBase     = 500 * time.Millisecond
	listRetryMax      = 4 * time.Second
)

// getPaged GETs a collection and follows Vercel's `pagination.next` cursor
// until the collection is exhausted.
//
// Following it internally, rather than handing NextPageToken back to the agent,
// is a deliberate trade. The SDK's List contract does support agent-driven
// paging, and it is the better shape for a single flat collection — but this
// engine also lists per parent, walking every project, and one opaque token
// cannot express "project 3, page 2" without inventing a composite cursor.
// Paging internally keeps that complexity out for the cost of a longer call. If
// an account ever grows big enough for that call to strain the watchdog, the
// flat-path case can move to real tokens without touching the parent walk.
func getPaged(ctx context.Context, c *vercelapi.Client, path, field, pageParam string, query map[string]string) ([]props, error) {
	var all []props
	cursor := ""

	// A cursor that never empties would spin forever. The cap is far above any
	// real collection, and hitting it is reported rather than silently
	// truncating the result — silent truncation is the bug this file exists to
	// prevent.
	const maxPages = 200

	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("%s: still paginating after %d pages; cursor is not advancing", path, maxPages)
		}
		q := query
		if cursor != "" {
			q = make(map[string]string, len(query)+1)
			for k, v := range query {
				q[k] = v
			}
			q[pageParam] = cursor
		}

		items, next, err := fetchListPage(ctx, c, path, field, q)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)

		// No declared page parameter means the caller cannot ask for more even
		// if the response offers a cursor.
		if next == "" || pageParam == "" {
			return all, nil
		}
		cursor = next
	}
}

// withListRetry repeats fn while the error is a throttle or a server fault.
func withListRetry[T any](ctx context.Context, what string, fn func() (T, error)) (T, error) {
	var zero T
	delay := listRetryBase
	for attempt := 1; ; attempt++ {
		out, err := fn()
		if err == nil {
			return out, nil
		}
		if attempt == listRetryAttempts || !vercelapi.IsRetryable(err) {
			return zero, err
		}
		select {
		case <-ctx.Done():
			return zero, fmt.Errorf("%s: %w", what, ctx.Err())
		case <-time.After(delay):
		}
		if delay *= 2; delay > listRetryMax {
			delay = listRetryMax
		}
	}
}
