package goworkflows

import (
	"strings"
	"testing"
)

// 9.6: the info-level retry record must never embed argv payloads
// (Jira/Orca request bodies or commands carrying prompts and RELAY_FLOW_*
// env, JQL). sanitizeRetryMessage strips each "[args...]: " span while
// preserving the surrounding wrap context and trailing stderr fragment.
func TestSanitizeRetryMessage(t *testing.T) {
	cases := []struct {
		name string
		in   string
		// mustNot: argv payloads that must be stripped
		mustNot []string
		// mustContain: failure context that must survive
		mustContain []string
	}{
		{
			name:        "acli comment with body payload",
			in:          `acli [jira workitem comment create --key PAY-1 --body SECRET-BODY --json]: exit status 1: permission denied`,
			mustNot:     []string{"SECRET-BODY", "--body", "--key PAY-1"},
			mustContain: []string{"acli", "exit status 1", "permission denied"},
		},
		{
			name:        "orca create with command payload",
			in:          `ensure run X: orca terminal create: orca [terminal create --worktree name:PAY-1 --title PAY-1:coding --command 'RELAY_FLOW_RUN_ID=r1 opencode --agent coder PROMPT-TEXT']: exit status 1: closed`,
			mustNot:     []string{"PROMPT-TEXT", "RELAY_FLOW_RUN_ID=r1", "--command", "--title PAY-1:coding"},
			mustContain: []string{"ensure run X", "orca terminal create", "exit status 1", "closed"},
		},
		{
			name:        "no brackets passes through",
			in:          "plain failure",
			mustContain: []string{"plain failure"},
		},
		{
			name:        "unterminated bracket passes through",
			in:          "weird [unterminated",
			mustContain: []string{"weird"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sanitizeRetryMessage(c.in)
			for _, s := range c.mustNot {
				if strings.Contains(got, s) {
					t.Fatalf("sanitizeRetryMessage leaked %q in %q", s, got)
				}
			}
			for _, s := range c.mustContain {
				if !strings.Contains(got, s) {
					t.Fatalf("sanitizeRetryMessage dropped %q from %q", s, got)
				}
			}
		})
	}
}
