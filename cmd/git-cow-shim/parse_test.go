package main

import (
	"reflect"
	"testing"
)

func TestRewrite(t *testing.T) {
	tests := []struct {
		name string
		args []string
		cow  bool
	}{
		{"t3", []string{"-c", "checkout.workers=8", "worktree", "add", "-b", "topic", "path with spaces 日本", "HEAD"}, true},
		{"cumulative cwd", []string{"-C", "parent", "-C", "child", "-c", "x.y=one two", "worktree", "add", "target"}, true},
		{"attached globals", []string{"-Crepo", "-ccheckout.workers=2", "worktree", "add", "target", "HEAD"}, true},
		{"detach", []string{"worktree", "add", "--detach", "target", "HEAD"}, true},
		{"force branch", []string{"worktree", "add", "-B", "topic", "--force", "target", "HEAD"}, true},
		{"lock reason", []string{"worktree", "add", "--lock", "--reason", "two words", "target"}, true},
		{"separator flags as paths", []string{"worktree", "add", "--", "--no-checkout", "--orphan"}, true},
		{"separator help as path", []string{"worktree", "add", "--", "--help"}, true},
		{"final checkout", []string{"worktree", "add", "--no-checkout", "--checkout", "target"}, true},
		{"no checkout", []string{"worktree", "add", "--no-checkout", "target"}, false},
		{"final no checkout", []string{"worktree", "add", "--checkout", "--no-checkout", "target"}, false},
		{"orphan", []string{"worktree", "add", "--orphan", "target"}, false},
		{"orphan negation conservatively bypasses", []string{"worktree", "add", "--orphan", "--no-orphan", "target"}, false},
		{"help", []string{"worktree", "add", "target", "--help"}, false},
		{"short help", []string{"worktree", "add", "-h"}, false},
		{"global help", []string{"--help", "worktree", "add", "target"}, false},
		{"global git dir", []string{"--git-dir", "repo/.git", "worktree", "add", "target"}, false},
		{"global work tree", []string{"--work-tree=repo", "worktree", "add", "target"}, false},
		{"global namespace", []string{"--namespace=foo", "worktree", "add", "target"}, false},
		{"global bare", []string{"--bare", "worktree", "add", "target"}, false},
		{"global exec path", []string{"--exec-path=/tmp", "worktree", "add", "target"}, false},
		{"global unknown", []string{"--unknown", "worktree", "add", "target"}, false},
		{"global separator", []string{"--", "worktree", "add", "target"}, false},
		{"config env", []string{"--config-env=x.y=Z", "worktree", "add", "target"}, false},
		{"empty C", []string{"-C", "", "worktree", "add", "target"}, false},
		{"missing C", []string{"-C"}, false},
		{"missing branch", []string{"worktree", "add", "-b"}, false},
		{"ambiguous branch", []string{"worktree", "add", "-b", "--no-checkout", "target"}, false},
		{"conflicting branch", []string{"worktree", "add", "-b", "a", "-B", "b", "target"}, false},
		{"missing reason", []string{"worktree", "add", "target", "--reason"}, false},
		{"unknown flag", []string{"worktree", "add", "--future-option", "target"}, false},
		{"attached branch is unknown", []string{"worktree", "add", "-btopic", "target"}, false},
		{"no path", []string{"worktree", "add"}, false},
		{"extra positional", []string{"worktree", "add", "target", "HEAD", "extra"}, false},
		{"remove", []string{"worktree", "remove", "target"}, false},
		{"status", []string{"status", "--porcelain"}, false},
		{"version", []string{"--version"}, false},
		{"empty", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := append([]string(nil), tt.args...)
			got, reason := rewrite(tt.args, false)
			if (reason == "cow") != tt.cow {
				t.Fatalf("rewrite(%q) = %q (%s), want cow=%v", tt.args, got, reason, tt.cow)
			}
			if !reflect.DeepEqual(before, tt.args) {
				t.Fatal("mutated input argv")
			}
			want := append([]string(nil), tt.args...)
			if tt.cow {
				for i, a := range want {
					if a == "worktree" {
						want[i] = "cow-worktree"
						break
					}
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %q, want %q", got, want)
			}
			bypassed, reason := rewrite(tt.args, true)
			if reason != "bypass" || !reflect.DeepEqual(bypassed, tt.args) {
				t.Fatal("bypass changed argv")
			}
		})
	}
}
