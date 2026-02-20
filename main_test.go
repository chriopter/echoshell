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

func TestRestoreSelectionMovesOffEmptyWorkspace(t *testing.T) {
	m := model{
		groups: []workspaceGroup{
			{Workspace: "root", Name: "root", Repo: "root", Sessions: nil},
			{Workspace: "git", Name: "git/app", Repo: "app", Sessions: []sessionInfo{{Name: "app-shell-1"}}},
		},
		selectedWorkspace: 0,
		selectedSession:   -1,
	}

	m.restoreSelection()

	if m.selectedWorkspace != 1 {
		t.Fatalf("expected selectedWorkspace to move to 1, got %d", m.selectedWorkspace)
	}
	if m.selectedSession != 0 {
		t.Fatalf("expected selectedSession to be 0, got %d", m.selectedSession)
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
