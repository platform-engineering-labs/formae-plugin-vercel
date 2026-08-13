// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package prov

import (
	"fmt"
	"strings"
)

// ParseTwoPart splits a composite native id like "{projectId}/{childId}" into
// its two segments. Either segment empty is an error.
func ParseTwoPart(id string) (parent, child string, err error) {
	parts := strings.SplitN(id, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("native id must be {projectId}/{id}, got %q", id)
	}
	return parts[0], parts[1], nil
}

// JoinTwoPart formats a composite native id.
func JoinTwoPart(parent, child string) string {
	return parent + "/" + child
}
