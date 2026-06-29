package main

import (
	"strings"
	"testing"
)

func TestAppendParentPreferences_EmptyRuleLeavesContentUnchanged(t *testing.T) {
	content := "# Agent\n\n## Child-Safety Rules (Critical)\n\n- be safe\n"
	for _, rule := range []string{"", "   ", "\n\t "} {
		if got := appendParentPreferences(content, rule); got != content {
			t.Fatalf("empty rule %q must leave content byte-identical; got:\n%s", rule, got)
		}
	}
}

func TestAppendParentPreferences_AddsSubordinateBlockAndPrecedenceFooter(t *testing.T) {
	content := "# Agent\n\n## Child-Safety Rules (Critical)\n\n- be safe\n"
	rule := "Bedtime is eight o'clock."

	got := appendParentPreferences(content, rule)

	if !strings.HasPrefix(got, content) {
		t.Fatalf("original content must be preserved as a prefix; got:\n%s", got)
	}
	if !strings.Contains(got, "## Parent Preferences") {
		t.Fatalf("expected Parent Preferences section; got:\n%s", got)
	}
	if !strings.Contains(got, rule) {
		t.Fatalf("expected the parent rule text; got:\n%s", got)
	}
	if !strings.Contains(got, "## Rule Precedence") {
		t.Fatalf("expected Rule Precedence footer; got:\n%s", got)
	}

	// The absolute precedence footer must come AFTER the parent rule text,
	// so the "rules above win" statement is the last word.
	ruleIdx := strings.Index(got, rule)
	precedenceIdx := strings.Index(got, "## Rule Precedence")
	if precedenceIdx < ruleIdx {
		t.Fatalf("precedence footer must follow the rule text (rule=%d, precedence=%d)", ruleIdx, precedenceIdx)
	}
}

func TestAppendParentPreferences_TrimsAndCapsLongRule(t *testing.T) {
	content := "# Agent\n"
	long := strings.Repeat("x", 800)

	got := appendParentPreferences(content, "  "+long+"  ")

	// The embedded rule must be trimmed and capped at parentRuleMaxLen.
	if strings.Contains(got, strings.Repeat("x", parentRuleMaxLen+1)) {
		t.Fatalf("rule must be capped at %d chars", parentRuleMaxLen)
	}
	if !strings.Contains(got, strings.Repeat("x", parentRuleMaxLen)) {
		t.Fatalf("expected rule capped to exactly %d chars", parentRuleMaxLen)
	}
}

func TestSanitizeParentRule_StripsFencesAndControlChars(t *testing.T) {
	got := sanitizeParentRule("```danger```\nbe\x00 kind\r\n")
	if strings.Contains(got, "`") {
		t.Fatalf("backticks must be stripped; got %q", got)
	}
	if strings.ContainsRune(got, '\x00') {
		t.Fatalf("control chars must be stripped; got %q", got)
	}
	if !strings.Contains(got, "be kind") {
		t.Fatalf("expected sanitized plain text to retain words; got %q", got)
	}
}
