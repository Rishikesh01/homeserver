package backup

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The retention policy is handed to `restic forget --prune` after every backup. restic treats any
// argument that isn't an option as a SNAPSHOT ID to delete — so a free-text policy like
// "keep f daily" deleted the snapshot whose ID starts with "f", and "7 daily, 4 weekly" was read
// as IDs that didn't match anything, which silently switched retention off. So the policy is
// parsed and checked here, and only well-formed restic options ever reach restic.

// retentionOpts are the restic forget options a policy may use, and what kind of value each takes.
var retentionOpts = map[string]string{
	"--keep-last": "count", "--keep-hourly": "count", "--keep-daily": "count",
	"--keep-weekly": "count", "--keep-monthly": "count", "--keep-yearly": "count",
	"--keep-within": "duration", "--keep-within-hourly": "duration", "--keep-within-daily": "duration",
	"--keep-within-weekly": "duration", "--keep-within-monthly": "duration", "--keep-within-yearly": "duration",
	"--keep-tag": "tag", "--group-by": "group",
}

var (
	durationRe = regexp.MustCompile(`^(\d+[ymdh])+$`)
	tagRe      = regexp.MustCompile(`^[A-Za-z0-9_.,:-]+$`)
	groupRe    = regexp.MustCompile(`^((host|paths|tags)(,(host|paths|tags))*)?$`)
)

const RetentionExample = "--keep-daily 7 --keep-weekly 4 --keep-monthly 6"

// ParseRetention checks a policy and returns it as restic arguments ("--opt", "value" pairs).
// It accepts "--opt value" and "--opt=value", and needs at least one --keep-* option.
func ParseRetention(policy string) ([]string, error) {
	fields := strings.Fields(policy)
	var args []string
	keeps := 0
	for i := 0; i < len(fields); i++ {
		opt, val, hasVal := strings.Cut(fields[i], "=")
		kind, ok := retentionOpts[opt]
		if !ok {
			return nil, fmt.Errorf("retention %q: %q isn't a restic --keep-* option (example: %s)",
				policy, fields[i], RetentionExample)
		}
		if !hasVal {
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("retention %q: %s needs a value", policy, opt)
			}
			i++
			val = fields[i]
		}
		if err := checkRetentionValue(opt, kind, val); err != nil {
			return nil, fmt.Errorf("retention %q: %w", policy, err)
		}
		if strings.HasPrefix(opt, "--keep-") {
			keeps++
		}
		args = append(args, opt, val)
	}
	if keeps == 0 {
		return nil, fmt.Errorf("retention %q keeps nothing — it needs at least one --keep-* option (example: %s)",
			policy, RetentionExample)
	}
	return args, nil
}

func checkRetentionValue(opt, kind, val string) error {
	switch kind {
	case "count":
		if n, err := strconv.Atoi(val); err != nil || n < -1 {
			return fmt.Errorf("%s needs a number (got %q)", opt, val)
		}
	case "duration":
		if !durationRe.MatchString(val) {
			return fmt.Errorf("%s needs a duration like 30d or 1y6m (got %q)", opt, val)
		}
	case "tag":
		if !tagRe.MatchString(val) {
			return fmt.Errorf("%s needs a tag (got %q)", opt, val)
		}
	case "group":
		if !groupRe.MatchString(val) {
			return fmt.Errorf("%s takes host, paths and/or tags (got %q)", opt, val)
		}
	}
	return nil
}

// ForgetArgs is the full `restic forget --prune …` command for a policy. Unless the policy says
// otherwise, snapshots are grouped by their tag: restic's default grouping (host + paths) starts a
// new, never-pruned group whenever the set of backed-up paths changes (a new app's volume, a new
// machine), so old snapshots would pile up forever.
func ForgetArgs(policy string) ([]string, error) {
	args, err := ParseRetention(policy)
	if err != nil {
		return nil, err
	}
	out := append([]string{"forget", "--prune"}, args...)
	if !strings.Contains(policy, "--group-by") {
		out = append(out, "--group-by", "tags")
	}
	return out, nil
}

// RetentionKeeps splits a policy into the daily/weekly/monthly counts the web form edits, plus the
// rest of the policy (kept as-is). Missing counts come back as -2 ("not set").
func RetentionKeeps(policy string) (daily, weekly, monthly int, rest string) {
	daily, weekly, monthly = -2, -2, -2
	args, err := ParseRetention(policy)
	if err != nil {
		return daily, weekly, monthly, ""
	}
	var other []string
	for i := 0; i+1 < len(args); i += 2 {
		n, _ := strconv.Atoi(args[i+1])
		switch args[i] {
		case "--keep-daily":
			daily = n
		case "--keep-weekly":
			weekly = n
		case "--keep-monthly":
			monthly = n
		default:
			other = append(other, args[i], args[i+1])
		}
	}
	return daily, weekly, monthly, strings.Join(other, " ")
}

// BuildRetention assembles a policy from the web form's counts (blank or 0 drops one) plus any
// other options the policy already had.
func BuildRetention(daily, weekly, monthly, rest string) (string, error) {
	var parts []string
	for _, kv := range [][2]string{{"--keep-daily", daily}, {"--keep-weekly", weekly}, {"--keep-monthly", monthly}} {
		v := strings.TrimSpace(kv[1])
		if v == "" || v == "0" {
			continue
		}
		if n, err := strconv.Atoi(v); err != nil || n < -1 {
			return "", fmt.Errorf("keep %s: %q isn't a number of snapshots (-1 keeps all)", strings.TrimPrefix(kv[0], "--keep-"), v)
		}
		parts = append(parts, kv[0], v)
	}
	if rest = strings.TrimSpace(rest); rest != "" {
		parts = append(parts, rest)
	}
	policy := strings.Join(parts, " ")
	if _, err := ParseRetention(policy); err != nil {
		return "", err
	}
	return policy, nil
}
