package main

import "strings"

// welcomeName is the signed-in Blaze account's first name for the start
// screen greeting ("" when signed out or unknown). Cached, never a network call.
func welcomeName() string {
	ac := getAccount()
	if ac == nil {
		return ""
	}
	u := ac.cachedUser()
	if u == nil {
		return ""
	}
	return strings.TrimSpace(u.Name)
}
