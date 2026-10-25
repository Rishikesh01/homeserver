package main

import (
	"slices"
	"strings"
	"testing"
)

// TestParseRetention pins the guard in front of `restic forget`: only well-formed --keep-* options
// get through, because anything else is taken by restic as a snapshot ID to delete.
func TestParseRetention(t *testing.T) {
	cases := []struct {
		policy string
		want   []string // nil = must be refused
	}{
		{defaultRetention, []string{"--keep-daily", "7", "--keep-weekly", "4", "--keep-monthly", "6"}},
		{"--keep-last=3", []string{"--keep-last", "3"}},
		{"--keep-within 1y6m --keep-tag x", []string{"--keep-within", "1y6m", "--keep-tag", "x"}},
		{"--keep-daily 7 --group-by host", []string{"--keep-daily", "7", "--group-by", "host"}},
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
		t.Run(tc.policy, func(t *testing.T) {
			got, err := parseRetention(tc.policy)
			if tc.want == nil {
				if err == nil {
					t.Fatalf("accepted %q as %v", tc.policy, got)
				}
				return
			}
			if err != nil || !slices.Equal(got, tc.want) {
				t.Errorf("= %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

// TestForgetArgs: snapshots are grouped by tag unless the policy says otherwise, so a new app's
// volume or a new machine doesn't start a group that's never pruned.
func TestForgetArgs(t *testing.T) {
	cases := []struct{ policy, want string }{
		{defaultRetention, "forget --prune " + defaultRetention + " --group-by tags"},
		{"--keep-daily 3 --group-by host", "forget --prune --keep-daily 3 --group-by host"},
	}
	for _, tc := range cases {
		t.Run(tc.policy, func(t *testing.T) {
			args, err := forgetArgs(tc.policy)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(args, " "); got != tc.want {
				t.Errorf("= %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBuildRetention: the web form's counts become a policy, keeping options the form doesn't show.
func TestBuildRetention(t *testing.T) {
	cases := []struct {
		name, daily, weekly, monthly, rest string
		want                               string // "" = must be refused
	}{
		{"edit counts, keep the rest", "14", "", "12", "--keep-last 2", "--keep-daily 14 --keep-monthly 12 --keep-last 2"},
		{"defaults", "7", "4", "6", "", defaultRetention},
		{"keeps nothing", "", "", "", "", ""},
		{"not a number", "x", "", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildRetention(tc.daily, tc.weekly, tc.monthly, tc.rest)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("accepted as %q", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("= %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	if d, w, m, rest := retentionKeeps("--keep-last 2 " + defaultRetention); d != 7 || w != 4 || m != 6 || rest != "--keep-last 2" {
		t.Errorf("retentionKeeps = %d %d %d %q", d, w, m, rest)
	}
}
