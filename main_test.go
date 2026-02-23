package main

import (
	"strings"
	"testing"
)

func TestAttachableSessionFromRepoRow(t *testing.T) {
	m := model{
		groups: []workspaceGroup{
			{Repo: "r", Sessions: []sessionInfo{{Name: "r-shell-1"}, {Name: "r-shell-2"}}},
		},
		selectedWorkspace: 0,
		selectedSession:   -1,
	}

	sel, ok := m.attachableSession()
	if !ok {
		t.Fatalf("expected attachable session")
	}
	if sel.Name != "r-shell-1" {
		t.Fatalf("expected first session, got %q", sel.Name)
	}
	if m.selectedSession != 0 {
		t.Fatalf("expected selectedSession to be 0, got %d", m.selectedSession)
	}
}

func TestAttachableSessionFallsBackToFirstWorkspaceWithSessions(t *testing.T) {
	m := model{
		groups: []workspaceGroup{
			{Repo: "root", Sessions: nil},
			{Repo: "app", Sessions: []sessionInfo{{Name: "app-shell-1"}}},
		},
		selectedWorkspace: 0,
		selectedSession:   -1,
	}

	sel, ok := m.attachableSession()
	if !ok {
		t.Fatalf("expected attachable session")
	}
	if sel.Name != "app-shell-1" {
		t.Fatalf("expected fallback session app-shell-1, got %q", sel.Name)
	}
	if m.selectedWorkspace != 1 {
		t.Fatalf("expected selectedWorkspace to move to 1, got %d", m.selectedWorkspace)
	}
	if m.selectedSession != 0 {
		t.Fatalf("expected selectedSession to be 0, got %d", m.selectedSession)
	}
}

func TestRestoreSelectionKeepsEmptyWorkspaceRow(t *testing.T) {
	m := model{
		groups: []workspaceGroup{
			{Workspace: "root", Name: "root", Repo: "root", Sessions: nil},
			{Workspace: "git", Name: "git/app", Repo: "app", Sessions: []sessionInfo{{Name: "app-shell-1"}}},
		},
		selectedWorkspace: 0,
		selectedSession:   -1,
		activeWorkspace:   "root",
	}

	m.restoreSelection()

	if m.selectedWorkspace != 0 {
		t.Fatalf("expected selectedWorkspace to stay on 0, got %d", m.selectedWorkspace)
	}
	if m.selectedSession != -1 {
		t.Fatalf("expected selectedSession to stay on repo row (-1), got %d", m.selectedSession)
	}
}

func TestRestoreSelectionKeepsRepoRowOnWorkspaceWithSessions(t *testing.T) {
	m := model{
		groups: []workspaceGroup{
			{Workspace: "git", Name: "git/app", Repo: "app", Sessions: []sessionInfo{{Name: "app-shell-1"}, {Name: "app-shell-2"}}},
		},
		selectedWorkspace: 0,
		selectedSession:   -1,
		activeWorkspace:   "git/app",
	}

	m.restoreSelection()

	if m.selectedWorkspace != 0 {
		t.Fatalf("expected selectedWorkspace to stay on 0, got %d", m.selectedWorkspace)
	}
	if m.selectedSession != -1 {
		t.Fatalf("expected selectedSession to stay on repo row (-1), got %d", m.selectedSession)
	}
}

func TestTmuxAttachCmdUsesSwitchClientInsideTmux(t *testing.T) {
	t.Setenv("TMUX", "1")
	cmd := tmuxAttachCmd("my-session")

	if len(cmd.Args) < 4 {
		t.Fatalf("unexpected args: %#v", cmd.Args)
	}
	if cmd.Args[1] != "switch-client" {
		t.Fatalf("expected switch-client, got %#v", cmd.Args)
	}
	if cmd.Args[3] != "my-session" {
		t.Fatalf("expected target my-session, got %#v", cmd.Args)
	}
}

func TestTmuxAttachCmdUsesAttachOutsideTmux(t *testing.T) {
	t.Setenv("TMUX", "")
	cmd := tmuxAttachCmd("my-session")

	if len(cmd.Args) < 4 {
		t.Fatalf("unexpected args: %#v", cmd.Args)
	}
	if cmd.Args[1] != "attach-session" {
		t.Fatalf("expected attach-session, got %#v", cmd.Args)
	}
	if cmd.Args[3] != "my-session" {
		t.Fatalf("expected target my-session, got %#v", cmd.Args)
	}
}

func TestDesiredTmuxMouseModeDefaultsOn(t *testing.T) {
	t.Setenv("ECHOSHELL_TMUX_MOUSE", "")
	mode, manage := desiredTmuxMouseMode()
	if !manage {
		t.Fatalf("expected mouse mode to be managed by default")
	}
	if mode != "on" {
		t.Fatalf("expected default mouse mode 'on', got %q", mode)
	}
}

func TestDesiredTmuxMouseModeOff(t *testing.T) {
	t.Setenv("ECHOSHELL_TMUX_MOUSE", "off")
	mode, manage := desiredTmuxMouseMode()
	if !manage {
		t.Fatalf("expected mouse mode to be managed for explicit off")
	}
	if mode != "off" {
		t.Fatalf("expected mouse mode 'off', got %q", mode)
	}
}

func TestDesiredTmuxMouseModeKeep(t *testing.T) {
	t.Setenv("ECHOSHELL_TMUX_MOUSE", "keep")
	mode, manage := desiredTmuxMouseMode()
	if manage {
		t.Fatalf("expected keep mode to skip managing mouse, got %q", mode)
	}
}

func TestTmuxListTargetsParsesNonEmptyUniqueLines(t *testing.T) {
	out := "s1\ns2\n\ns2\n  s3  \n"
	targets := parseTmuxTargets(out)
	if len(targets) != 3 {
		t.Fatalf("expected 3 targets, got %d (%#v)", len(targets), targets)
	}
	if targets[0] != "s1" || targets[1] != "s2" || targets[2] != "s3" {
		t.Fatalf("unexpected targets: %#v", targets)
	}
}

func TestSoftAttachPaneCommandUsesReadOnlyAttach(t *testing.T) {
	cmd := softAttachPaneCommand("my-session")
	if !strings.Contains(cmd, "TMUX=") {
		t.Fatalf("preview command should clear TMUX: %q", cmd)
	}
	if !strings.Contains(cmd, "attach-session") {
		t.Fatalf("preview command should attach session: %q", cmd)
	}
	if !strings.Contains(cmd, "-r") {
		t.Fatalf("preview command should be read-only: %q", cmd)
	}
}

func TestScoreSessionMatchTwoArgsUseRepoThenSession(t *testing.T) {
	g := workspaceGroup{Workspace: "git", Repo: "opasdf", Name: "git/opasdf"}
	lazy := sessionInfo{Name: "opasdf-lazygit-1", Workdir: "/root/git/opasdf"}
	shell := sessionInfo{Name: "opasdf-shell-1", Workdir: "/root/git/opasdf"}

	if _, ok := scoreSessionMatch([]string{"op", "la"}, g, lazy); !ok {
		t.Fatalf("expected lazygit session to match repo+session query")
	}
	if _, ok := scoreSessionMatch([]string{"op", "la"}, g, shell); ok {
		t.Fatalf("did not expect shell session to match session query 'la'")
	}
}

func TestScoreSessionMatchTwoArgsRequireRepoMatch(t *testing.T) {
	g := workspaceGroup{Workspace: "git", Repo: "tools", Name: "git/tools"}
	s := sessionInfo{Name: "tools-lazygit-1", Workdir: "/root/git/tools"}

	if _, ok := scoreSessionMatch([]string{"op", "la"}, g, s); ok {
		t.Fatalf("did not expect repo mismatch to match query")
	}
}
