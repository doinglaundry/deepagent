package middleware

import (
	"context"
	"eino-cli/deepagent/core/backend"
	"testing"
)

func TestFilesystemCommandClassificationRules(t *testing.T) {
	cases := []struct {
		command string
		want    commandClassification
	}{
		{"", classificationUnknown},
		{"ls -la | head -5", classificationSafe},
		{"printf '%s' 'a>b'", classificationSafe},
		{"sh -c 'git status && ls'", classificationSafe},
		{"git diff --stat", classificationSafe},
		{"find . -name '*.go'", classificationSafe},
		{"echo $(whoami)", classificationDangerous},
		{"echo `whoami`", classificationDangerous},
		{"echo changed > file", classificationDangerous},
		{"ls; rm file", classificationDangerous},
		{"sh -c 'rm file'", classificationDangerous},
		{"rm -rf /", classificationForbidden},
		{"git -c core.pager=custom log", classificationDangerous},
		{"git branch -D feature", classificationDangerous},
		{"git diff --ext-diff", classificationDangerous},
		{"sed -i 's/a/b/' file", classificationDangerous},
		{"find . -delete", classificationDangerous},
		{"find . -exec touch marker \\;", classificationDangerous},
		{"curl https://example.com", classificationDangerous},
		{"python3 -c 'print(1)'", classificationDangerous},
		{"unrecognized-tool arg", classificationUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			got := classifyShellScript(context.Background(), tc.command)
			if got.Classification != tc.want {
				t.Fatalf("classification=%s reason=%s want=%s", got.Classification, got.Reason, tc.want)
			}
		})
	}
}

type classificationCommandProbe struct {
	backend.CommandService
	calls int
}

func (p *classificationCommandProbe) Start(context.Context, backend.CommandRequest) (string, error) {
	p.calls++
	return "job", nil
}
func (p *classificationCommandProbe) Execute(context.Context, backend.CommandRequest) (*backend.CommandResult, error) {
	p.calls++
	return &backend.CommandResult{}, nil
}
func TestGitBranchMutationsDoNotReachCommandService(t *testing.T) {
	cases := []struct {
		command string
		allowed bool
	}{
		{"git branch", true}, {"git branch -avv", true}, {"git branch --list 'feature/*'", true},
		{"git branch --show-current", true}, {"git branch --contains HEAD", true},
		{"git branch --sort=-committerdate --format='%(refname:short)'", true},
		{"git branch --sort=refname created", false},
		{"git branch --format=short created", false},
		{"git branch created", false}, {"git branch created HEAD", false},
		{"git branch -f existing HEAD", false}, {"git branch --set-upstream-to=origin/main existing", false},
		{"git branch --unset-upstream", false}, {"git branch --edit-description", false},
		{"git branch -Dm old", false}, {"git branch --list -D old", false},
		{"git branch --list -- feature-*", true}, {"git branch -- created", false},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			for _, start := range []bool{false, true} {
				probe := &classificationCommandProbe{}
				service := classifiedCommands{CommandService: probe}
				var err error
				if start {
					_, err = service.Start(context.Background(), backend.CommandRequest{Command: tc.command})
				} else {
					_, err = service.Execute(context.Background(), backend.CommandRequest{Command: tc.command})
				}
				if tc.allowed {
					if err != nil || probe.calls != 1 {
						t.Fatalf("query rejected: err=%v calls=%d", err, probe.calls)
					}
				} else if err == nil || probe.calls != 0 {
					t.Fatalf("mutation reached service: err=%v calls=%d", err, probe.calls)
				}
			}
		})
	}
}
