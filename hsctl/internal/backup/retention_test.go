//go:build integration

package backup_test

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/backup"
)

// TestParseRetention guards `restic forget`: only well-formed --keep-* options get through, because
// anything else is taken by restic as a snapshot ID to delete.
func TestParseRetention(t *testing.T) {
	cases := []struct {
		policy string
		want   []string // nil = must be refused
	}{
		{backup.DefaultRetention, []string{"--keep-daily", "7", "--keep-weekly", "4", "--keep-monthly", "6"}},
		{"--keep-last=3", []string{"--keep-last", "3"}},
		{"--keep-within 1y6m --keep-tag x", []string{"--keep-within", "1y6m", "--keep-tag", "x"}},
		{"--keep-daily 7 --group-by host", []string{"--keep-daily", "7", "--group-by", "host"}},
		{"--keep-daily -1", []string{"--keep-daily", "-1"}},
		{"keep f daily", nil},        // deleted snapshot f46887bb before this guard
		{"7 daily, 4 weekly", nil},   // silently disabled retention
		{"--keep-daily", nil},        // missing value
		{"--keep-daily seven", nil},  // not a number
		{"--keep-within 30", nil},    // not a duration
		{"--group-by host", nil},     // keeps nothing
		{"--keep-daily 7 abc1", nil}, // a stray snapshot ID
		{"", nil},
	}
	for _, tc := range cases {
		got, err := backup.ParseRetention(tc.policy)
		if tc.want == nil {
			if err == nil {
				t.Errorf("accepted %q as %v", tc.policy, got)
			}
			continue
		}
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("ParseRetention(%q) = %v, %v; want %v", tc.policy, got, err, tc.want)
		}
	}
}

// TestForgetArgs groups snapshots by tag unless the policy says otherwise, so a new app's volume or
// a new machine doesn't start a group that's never pruned.
func TestForgetArgs(t *testing.T) {
	cases := []struct{ policy, want string }{
		{backup.DefaultRetention, "forget --prune " + backup.DefaultRetention + " --group-by tags"},
		{"--keep-daily 3 --group-by host", "forget --prune --keep-daily 3 --group-by host"},
		{"nonsense", ""},
	}
	for _, tc := range cases {
		args, err := backup.ForgetArgs(tc.policy)
		if got := strings.Join(args, " "); got != tc.want || (err != nil) != (tc.want == "") {
			t.Errorf("ForgetArgs(%q) = %q, %v; want %q", tc.policy, got, err, tc.want)
		}
	}
}

// TestBuildRetention: the web form's counts become a policy, keeping options the form doesn't show.
func TestBuildRetention(t *testing.T) {
	cases := []struct {
		name, daily, weekly, monthly, rest string
		want                               string // "" = must be refused
	}{
		{"edit counts, keep the rest", "14", "", "12", "--keep-last 2", "--keep-daily 14 --keep-monthly 12 --keep-last 2"},
		{"defaults", "7", "4", "6", "", backup.DefaultRetention},
		{"keeps nothing", "", "", "", "", ""},
		{"not a number", "x", "", "", "", ""},
		{"keep all dailies", "-1", "", "12", "", "--keep-daily -1 --keep-monthly 12"},
	}
	for _, tc := range cases {
		got, err := backup.BuildRetention(tc.daily, tc.weekly, tc.monthly, tc.rest)
		if got != tc.want || (err != nil) != (tc.want == "") {
			t.Errorf("%s: BuildRetention = %q, %v; want %q", tc.name, got, err, tc.want)
		}
		if tc.want == "" {
			continue
		}
		// What the form shows for that policy is what was typed.
		d, w, m, rest := backup.RetentionKeeps(got)
		if rest != tc.rest || (tc.daily != "" && strconv.Itoa(d) != tc.daily) || (tc.weekly != "" && strconv.Itoa(w) != tc.weekly) || (tc.monthly != "" && strconv.Itoa(m) != tc.monthly) {
			t.Errorf("%s: RetentionKeeps(%q) = %d %d %d %q", tc.name, got, d, w, m, rest)
		}
	}
}
