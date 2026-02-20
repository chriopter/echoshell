package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const refreshInterval = 2 * time.Second
const defaultRemoteTarget = "local"
const maxPreviewLines = 8

var selectedRemoteTarget = ""
var updateRepoDir = ""
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
var repoGroupCache = map[string][]workspaceGroup{}

type sessionInfo struct {
	Name     string
	Workdir  string
	Attached bool
	Windows  int
}

type workspaceGroup struct {
	Workspace string
	Repo      string
	Name      string
	Path      string
	Sessions  []sessionInfo
}

type loadedMsg struct {
	groups []workspaceGroup
	err    error
}

type tickMsg time.Time

type actionMsg struct {
	status string
	err    error
}

type createdMsg struct {
	name   string
	status string
	err    error
}

type viewCreatedMsg struct {
	name  string
	count int
	err   error
}

type previewMsg struct {
	session string
	text    string
	err     error
}

type softAttachMsg struct {
	pane    string
	session string
	err     error
}

type remoteMsg struct {
	target string
	err    error
}

type quickCandidate struct {
	Workspace string
	Repo      string
	Session   sessionInfo
	Score     int
}

type sessionTemplate struct {
	Label   string
	Name    string
	Command string
}

type menuItem struct {
	Label string
	Key   string
}

type model struct {
	width              int
	height             int
	groups             []workspaceGroup
	selectedWorkspace  int
	selectedSession    int // -1 means repo row selected
	multiSelected      map[string]bool
	previewErr         bool
	selectingMenu      bool
	menuItems          []menuItem
	selectedMenu       int
	preferredWorkspace string
	activeWorkspace    string
	activeSession      string
	previewSession     string
	previewText        string
	previewPane        string
	updateBusy         bool
	status             string
	selectingRemote    bool
	availableTargets   []string
	selectedTarget     int
	addingNewRemote    bool
	newRemoteInput     string
	selectingNew       bool
	newTemplates       []sessionTemplate
	selectedTemplate   int
	selectingQuick     bool
	quickQuery         string
	quickCandidates    []quickCandidate
	selectedQuick      int
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return errors.New("tmux is required")
	}
	if started, err := bootstrapIntoTmuxIfNeeded(); started {
		return err
	}

	selectedRemoteTarget = defaultRemoteTarget

	updateRepoDir = detectRepoDir()
	preferredWorkspace, _ := loadLastWorkspaceTarget(selectedRemoteTarget)

	m := model{
		status:             "Loading sessions...",
		selectingRemote:    false,
		availableTargets:   []string{"local"},
		selectedTarget:     0,
		preferredWorkspace: preferredWorkspace,
		newTemplates:       defaultSessionTemplates(),
		multiSelected:      map[string]bool{},
	}

	if len(os.Args) > 1 {
		matches, qerr := findQuickCandidates(os.Args[1:])
		if qerr == nil && len(matches) == 1 {
			return attachSessionNow(matches[0].Session.Name)
		}
		if qerr == nil && len(matches) > 1 {
			m.selectingQuick = true
			m.quickQuery = strings.Join(os.Args[1:], " ")
			m.quickCandidates = matches
			m.selectedQuick = 0
			m.status = fmt.Sprintf("%d matches for %q", len(matches), m.quickQuery)
		}
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func bootstrapIntoTmuxIfNeeded() (bool, error) {
	if strings.TrimSpace(os.Getenv("TMUX")) != "" {
		return false, nil
	}
	if strings.TrimSpace(os.Getenv("ECHOSHELL_IN_TMUX")) == "1" {
		return false, nil
	}
	if strings.TrimSpace(os.Getenv("ECHOSHELL_AUTO_TMUX")) == "0" {
		return false, nil
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		return false, nil
	}

	cmdline := "ECHOSHELL_IN_TMUX=1 " + shellQuote(os.Args[0])
	for _, a := range os.Args[1:] {
		cmdline += " " + shellQuote(a)
	}

	sessionName := "echoshell"
	if err := exec.Command("tmux", "has-session", "-t", sessionName).Run(); err == nil {
		sessionName = fmt.Sprintf("echoshell-%d", os.Getpid())
	}

	cmd := exec.Command("tmux", "new-session", "-s", sessionName, cmdline)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return true, cmd.Run()
}

func loadTargetsForSelection(lastTarget string) ([]string, int) {
	targets, err := loadAllTargets()
	if err != nil || len(targets) == 0 {
		targets = []string{"local"}
	}
	targets = append(targets, "+ Add new remote...")
	selectedIdx := 0
	for i, t := range targets {
		if t == lastTarget {
			selectedIdx = i
			break
		}
	}
	return targets, selectedIdx
}

func findQuickCandidates(tokens []string) ([]quickCandidate, error) {
	groups, err := groupedSessions()
	if err != nil {
		return nil, err
	}
	out := []quickCandidate{}
	for _, g := range groups {
		for _, s := range g.Sessions {
			score, ok := scoreSessionMatch(tokens, g, s)
			if !ok {
				continue
			}
			out = append(out, quickCandidate{
				Workspace: groupWorkspaceName(g),
				Repo:      g.Repo,
				Session:   s,
				Score:     score,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Session.Name != out[j].Session.Name {
			return out[i].Session.Name < out[j].Session.Name
		}
		if out[i].Workspace != out[j].Workspace {
			return out[i].Workspace < out[j].Workspace
		}
		return out[i].Repo < out[j].Repo
	})
	return out, nil
}

func scoreSessionMatch(tokens []string, g workspaceGroup, s sessionInfo) (int, bool) {
	t := normalizeForMatch(strings.Join(tokens, " "))
	if t == "" {
		return 0, false
	}
	parts := strings.Fields(t)
	if len(parts) == 0 {
		return 0, false
	}
	hay := normalizeForMatch(strings.Join([]string{s.Name, g.Name, g.Workspace, g.Repo, s.Workdir}, " "))
	score := 0
	for _, p := range parts {
		if strings.Contains(hay, p) {
			score += 10 + len(p)
			continue
		}
		if subseq(hay, p) {
			score += 4 + len(p)
			continue
		}
		return 0, false
	}
	if strings.Contains(hay, normalizeForMatch(s.Name)) {
		score += 3
	}
	return score, true
}

func normalizeForMatch(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	space := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

func subseq(hay, needle string) bool {
	if needle == "" {
		return true
	}
	h := []rune(hay)
	n := []rune(needle)
	j := 0
	for i := 0; i < len(h) && j < len(n); i++ {
		if h[i] == n[j] {
			j++
		}
	}
	return j == len(n)
}

func (m model) Init() tea.Cmd {
	if m.selectingRemote || m.selectingQuick {
		return nil
	}
	return tea.Batch(loadCmd(), tickCmd())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Handle remote selection mode
	if m.selectingRemote {
		switch msg := msg.(type) {
		case tea.WindowSizeMsg:
			m.width = msg.Width
			m.height = msg.Height
			return m, nil
		case tea.KeyMsg:
			// Handle text input mode for new remote
			if m.addingNewRemote {
				switch msg.String() {
				case "ctrl+c", "esc":
					m.addingNewRemote = false
					m.newRemoteInput = ""
					return m, nil
				case "enter":
					if strings.TrimSpace(m.newRemoteInput) != "" {
						selectedRemoteTarget = strings.TrimSpace(m.newRemoteInput)
						_ = rememberRemoteTarget(selectedRemoteTarget)
						if isLocalRemote() {
							if _, err := exec.LookPath("tmux"); err != nil {
								m.status = "Error: tmux is required for local mode"
								m.addingNewRemote = false
								m.newRemoteInput = ""
								return m, nil
							}
						}
						m.selectingRemote = false
						m.addingNewRemote = false
						m.newRemoteInput = ""
						m.preferredWorkspace, _ = loadLastWorkspaceTarget(selectedRemoteTarget)
						m.status = "Loading sessions..."
						return m, tea.Batch(loadCmd(), tickCmd())
					}
					return m, nil
				case "backspace":
					if len(m.newRemoteInput) > 0 {
						m.newRemoteInput = m.newRemoteInput[:len(m.newRemoteInput)-1]
					}
					return m, nil
				default:
					// Accept printable characters
					if len(msg.String()) == 1 {
						m.newRemoteInput += msg.String()
					}
					return m, nil
				}
			}

			switch strings.ToLower(msg.String()) {
			case "ctrl+c", "q", "esc":
				cleanupSoftPreview(&m)
				return m, tea.Quit
			case "up", "k":
				if m.selectedTarget > 0 {
					m.selectedTarget--
				}
				return m, nil
			case "down", "j":
				if m.selectedTarget < len(m.availableTargets)-1 {
					m.selectedTarget++
				}
				return m, nil
			case "enter":
				selected := m.availableTargets[m.selectedTarget]
				// Check if "+ Add new..." was selected
				if strings.HasPrefix(selected, "+ Add new") {
					m.addingNewRemote = true
					m.newRemoteInput = ""
					return m, nil
				}
				selectedRemoteTarget = selected
				_ = rememberRemoteTarget(selectedRemoteTarget)
				if isLocalRemote() {
					if _, err := exec.LookPath("tmux"); err != nil {
						m.status = "Error: tmux is required for local mode"
						return m, nil
					}
				}
				m.selectingRemote = false
				m.preferredWorkspace, _ = loadLastWorkspaceTarget(selectedRemoteTarget)
				m.activeWorkspace = ""
				m.activeSession = ""
				m.status = "Loading sessions..."
				return m, tea.Batch(loadCmd(), tickCmd())
			}
		}
		return m, nil
	}

	if m.selectingNew {
		switch msg := msg.(type) {
		case tea.WindowSizeMsg:
			m.width = msg.Width
			m.height = msg.Height
			return m, nil
		case tea.KeyMsg:
			switch strings.ToLower(msg.String()) {
			case "ctrl+c", "q":
				cleanupSoftPreview(&m)
				return m, tea.Quit
			case "esc":
				m.selectingNew = false
				m.status = "Cancelled new session"
				return m, nil
			case "up", "k":
				if m.selectedTemplate > 0 {
					m.selectedTemplate--
				}
				return m, nil
			case "down", "j":
				if m.selectedTemplate < len(m.newTemplates)-1 {
					m.selectedTemplate++
				}
				return m, nil
			case "enter":
				if len(m.newTemplates) == 0 {
					m.selectingNew = false
					m.status = "No session templates"
					return m, nil
				}
				tpl := m.newTemplates[m.selectedTemplate]
				m.selectingNew = false
				m.status = "Creating " + tpl.Label + " session..."
				return m, newSessionCmd(m.newSessionPath(), m.newSessionRepo(), tpl.Name, tpl.Command)
			}
		}
		return m, nil
	}

	if m.selectingQuick {
		switch msg := msg.(type) {
		case tea.WindowSizeMsg:
			m.width = msg.Width
			m.height = msg.Height
			return m, nil
		case tea.KeyMsg:
			switch strings.ToLower(msg.String()) {
			case "ctrl+c", "q", "esc":
				cleanupSoftPreview(&m)
				return m, tea.Quit
			case "up", "k":
				if m.selectedQuick > 0 {
					m.selectedQuick--
				}
				return m, nil
			case "down", "j":
				if m.selectedQuick < len(m.quickCandidates)-1 {
					m.selectedQuick++
				}
				return m, nil
			case "enter":
				if len(m.quickCandidates) == 0 {
					cleanupSoftPreview(&m)
					return m, tea.Quit
				}
				name := m.quickCandidates[m.selectedQuick].Session.Name
				m.status = "Attaching " + name + "..."
				cleanupSoftPreview(&m)
				return m, attachCmd(name)
			}
		}
		return m, nil
	}

	if m.selectingMenu {
		switch msg := msg.(type) {
		case tea.WindowSizeMsg:
			m.width = msg.Width
			m.height = msg.Height
			return m, nil
		case tea.KeyMsg:
			switch strings.ToLower(msg.String()) {
			case "0":
				m.selectingMenu = false
				m.status = ""
				return m, nil
			case "up":
				if m.selectedMenu > 0 {
					m.selectedMenu--
				}
				return m, nil
			case "down":
				if m.selectedMenu < len(m.menuItems)-1 {
					m.selectedMenu++
				}
				return m, nil
			case "enter":
				if len(m.menuItems) == 0 {
					m.selectingMenu = false
					return m, nil
				}
				key := m.menuItems[m.selectedMenu].Key
				m.selectingMenu = false
				switch key {
				case "attach":
					sel, ok := m.selectedSessionInfo()
					if !ok {
						return m, nil
					}
					m.status = "Attaching " + sel.Name + "..."
					cleanupSoftPreview(&m)
					return m, attachCmd(sel.Name)
				case "refresh":
					m.status = "Refreshing..."
					return m, loadCmd()
				case "update":
					if m.updateBusy {
						return m, nil
					}
					m.updateBusy = true
					m.status = "Updating from origin/main..."
					return m, updateCmd()
				case "destroy":
					sel, ok := m.selectedSessionInfo()
					if !ok {
						return m, nil
					}
					m.status = "Destroying " + sel.Name + "..."
					return m, killSessionCmd(sel.Name)
				case "quit":
					cleanupSoftPreview(&m)
					return m, tea.Quit
				}
				return m, nil
			}
		}
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case loadedMsg:
		if msg.err != nil {
			m.status = "Refresh failed: " + msg.err.Error()
			return m, nil
		}
		m.groups = msg.groups
		m.restoreSelection()
		if len(m.groups) == 0 {
			m.status = "No repo entries"
		} else {
			m.status = fmt.Sprintf("Loaded %d repo entries", len(m.groups))
		}
		return m, previewCmdForSelection(m)

	case tickMsg:
		return m, tea.Batch(loadCmd(), tickCmd())

	case actionMsg:
		m.updateBusy = false
		if msg.err != nil {
			m.status = "Action failed: " + msg.err.Error()
			return m, nil
		}
		m.status = msg.status
		return m, loadCmd()

	case createdMsg:
		if msg.err != nil {
			m.status = "Action failed: " + msg.err.Error()
			return m, nil
		}
		if len(m.groups) > 0 && m.selectedWorkspace >= 0 && m.selectedWorkspace < len(m.groups) {
			m.activeWorkspace = m.groups[m.selectedWorkspace].Name
		}
		m.activeSession = msg.name
		m.status = msg.status
		return m, loadCmd()

	case viewCreatedMsg:
		if msg.err != nil {
			m.status = "Action failed: " + msg.err.Error()
			return m, nil
		}
		m.multiSelected = nil
		m.status = fmt.Sprintf("Opened split view (%d panes): %s", msg.count, msg.name)
		cleanupSoftPreview(&m)
		return m, attachCmd(msg.name)

	case previewMsg:
		if m.softAttachPreviewEnabled() {
			return m, nil
		}
		if msg.err != nil {
			m.previewSession = msg.session
			m.previewText = "Preview error: " + msg.err.Error()
			m.previewErr = true
			return m, nil
		}
		m.previewSession = msg.session
		m.previewText = strings.TrimRight(msg.text, "\n")
		m.previewErr = false
		if strings.TrimSpace(m.previewText) == "" {
			m.previewText = "(no output yet)"
		}
		return m, nil

	case softAttachMsg:
		if msg.err != nil {
			m.status = "Soft attach failed: " + msg.err.Error()
			return m, nil
		}
		m.previewPane = msg.pane
		m.previewSession = msg.session
		return m, nil

	case remoteMsg:
		if msg.err != nil {
			m.status = "Remote change failed: " + msg.err.Error()
			return m, nil
		}
		cleanupSoftPreview(&m)
		selectedRemoteTarget = msg.target
		_ = rememberRemoteTarget(selectedRemoteTarget)
		m.preferredWorkspace, _ = loadLastWorkspaceTarget(selectedRemoteTarget)
		m.activeWorkspace = ""
		m.activeSession = ""
		m.status = "Switched to remote: " + remoteTarget()
		return m, loadCmd()

	case tea.KeyMsg:
		switch strings.ToLower(msg.String()) {
		case "ctrl+c":
			cleanupSoftPreview(&m)
			return m, tea.Quit
		case "d":
			sel, ok := m.selectedSessionInfo()
			if !ok {
				return m, nil
			}
			m.status = "Destroying " + sel.Name + "..."
			return m, killSessionCmd(sel.Name)
		case "0":
			m.menuItems = buildMenuItems(m)
			m.selectedMenu = 0
			m.selectingMenu = true
			m.status = "Menu"
			return m, nil
		case "tab":
			if m.shiftRepo(1) {
				return m, previewCmdForSelection(m)
			}
			return m, nil
		case "shift+tab":
			if m.shiftRepo(-1) {
				return m, previewCmdForSelection(m)
			}
			return m, nil
		case "left":
			if m.shiftRepo(-1) {
				return m, previewCmdForSelection(m)
			}
			return m, nil
		case "right":
			if m.shiftRepo(1) {
				return m, previewCmdForSelection(m)
			}
			return m, nil
		case "up":
			if m.shiftVertical(-1) {
				return m, previewCmdForSelection(m)
			}
			return m, nil
		case "down":
			if m.shiftVertical(1) {
				return m, previewCmdForSelection(m)
			}
			return m, nil
		case "enter":
			sel, ok := m.selectedSessionInfo()
			if !ok {
				return m, nil
			}
			m.status = "Attaching " + sel.Name + "..."
			cleanupSoftPreview(&m)
			return m, attachCmd(sel.Name)
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			s := strings.ToLower(msg.String())
			idx := int(s[0] - '1')
			repos := m.repoOrder()
			if idx >= 0 && idx < len(repos) {
				m.selectedWorkspace = repos[idx]
				m.selectedSession = defaultSessionIndex(m.groups[m.selectedWorkspace].Sessions)
				m.captureActive()
				return m, previewCmdForSelection(m)
			}
			return m, nil
		case "o":
			return m, spawnAndAttachCmd(m, "opencode", "opencode")
		case "l":
			return m, spawnAndAttachCmd(m, "lazygit", "lazygit")
		case "c":
			return m, spawnAndAttachCmd(m, "claude-full", "IS_SANDBOX=1 claude --dangerously-skip-permissions")
		case "b":
			return m, spawnAndAttachCmd(m, "shell", "")
		}
	}

	return m, nil
}

func (m model) View() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Render("echoshell")

	// Remote selection view
	if m.selectingRemote {
		// Text input mode for new remote
		if m.addingNewRemote {
			help := lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Render("enter: confirm  esc: cancel")
			heading := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220")).Render("Enter new remote (e.g., user@host):")

			cursor := lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Render("▊")
			inputLine := m.newRemoteInput + cursor
			inputStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("111")).Padding(0, 1)

			lines := []string{
				heading,
				"",
				inputStyle.Render(inputLine),
			}

			box := lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("240")).Padding(1, 2).Render(strings.Join(lines, "\n"))
			return lipgloss.JoinVertical(lipgloss.Left, title, "", box, "", help)
		}

		help := lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Render("j/k or ↑/↓: navigate  enter: select  q: quit")
		heading := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220")).Render("Select Remote Target:")

		sel := lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62")).Padding(0, 1)
		norm := lipgloss.NewStyle().Padding(0, 1)

		lines := []string{heading, ""}
		for i, target := range m.availableTargets {
			line := target
			if i == m.selectedTarget {
				lines = append(lines, sel.Render(line))
			} else {
				lines = append(lines, norm.Render(line))
			}
		}

		box := lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("240")).Padding(1, 2).Render(strings.Join(lines, "\n"))
		return lipgloss.JoinVertical(lipgloss.Left, title, "", box, "", help)
	}

	if m.selectingMenu {
		heading := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220")).Render("Menu")
		help := lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Render("up/down: navigate  enter: run  0: close")
		sel := lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62")).Padding(0, 1)
		norm := lipgloss.NewStyle().Padding(0, 1)

		lines := []string{heading, ""}
		for i, it := range m.menuItems {
			if i == m.selectedMenu {
				lines = append(lines, sel.Render(it.Label))
			} else {
				lines = append(lines, norm.Render(it.Label))
			}
		}
		box := lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("240")).Padding(1, 2).Render(strings.Join(lines, "\n"))
		return lipgloss.JoinVertical(lipgloss.Left, title, "", box, "", help)
	}

	if m.selectingNew {
		help := lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Render("j/k or ↑/↓: navigate  enter: create  esc: cancel")
		heading := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220")).Render("New Session Command:")

		sel := lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62")).Padding(0, 1)
		norm := lipgloss.NewStyle().Padding(0, 1)

		lines := []string{heading, ""}
		for i, t := range m.newTemplates {
			line := t.Label
			if i == m.selectedTemplate {
				lines = append(lines, sel.Render(line))
			} else {
				lines = append(lines, norm.Render(line))
			}
		}

		box := lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("240")).Padding(1, 2).Render(strings.Join(lines, "\n"))
		return lipgloss.JoinVertical(lipgloss.Left, title, "", box, "", help)
	}

	if m.selectingQuick {
		heading := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220")).Render("Quick Attach: " + m.quickQuery)
		help := lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Render("j/k or ↑/↓: navigate  enter: attach  esc: quit")
		sel := lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62")).Padding(0, 1)
		norm := lipgloss.NewStyle().Padding(0, 1)
		lines := []string{heading, ""}
		for i, c := range m.quickCandidates {
			line := fmt.Sprintf("%s  (%s/%s)", c.Session.Name, c.Workspace, c.Repo)
			if i == m.selectedQuick {
				lines = append(lines, sel.Render(line))
			} else {
				lines = append(lines, norm.Render(line))
			}
		}
		box := lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("240")).Padding(1, 2).Render(strings.Join(lines, "\n"))
		return lipgloss.JoinVertical(lipgloss.Left, title, "", box, "", help)
	}

	remote := lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Render("remote: " + remoteTarget())
	helpNav := lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Render("1-9 repo  tab repo  arrows nav (soft attach right)  enter full attach  d destroy  0 menu  o opencode  l lazygit  c claude  b bash")
	help := helpNav
	status := lipgloss.NewStyle().Foreground(lipgloss.Color("111")).Render("status: " + m.status)

	if len(m.groups) == 0 {
		empty := lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Padding(1, 2).Render("No sessions")
		return lipgloss.JoinVertical(lipgloss.Left, title, remote, empty, status, help)
	}

	leftW := 30
	if m.width > 0 {
		leftW = max(34, m.width-4)
	}
	bodyH := 0
	// Layout is: title (1) + remote (1) + body + status (1) + help (2)
	if m.height > 0 {
		bodyH = max(8, m.height-5)
	}

	left := m.renderWorkspaces(leftW, bodyH)
	body := left

	return lipgloss.JoinVertical(lipgloss.Left, title, remote, body, status, help)
}

func (m model) renderWorkspaces(width, height int) string {
	box := lipgloss.NewStyle().Width(width).Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("240")).Padding(1, 2)
	if height > 0 {
		box = box.Height(height)
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220")).Render("Repos")

	repoSel := lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62")).Padding(0, 1)
	sessSel := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Padding(0, 1)
	sessNorm := lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Padding(0, 1)

	lines := []string{title, ""}
	for i, g := range m.groups {
		markerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(repoColor(g.Repo))).Bold(true)
		marker := " "
		if i == m.selectedWorkspace {
			marker = "|"
		}
		repoLine := fmt.Sprintf("%s %s (%d)", markerStyle.Render(marker), g.Repo, len(g.Sessions))
		repoColor := lipgloss.NewStyle().Foreground(lipgloss.Color(repoColor(g.Repo))).Padding(0, 1)
		if i == m.selectedWorkspace {
			lines = append(lines, repoSel.Render(repoLine))
		} else {
			lines = append(lines, repoColor.Render(repoLine))
		}

		for si, s := range g.Sessions {
			att := " "
			if s.Attached {
				att = "*"
			}
			name := trimRepoPrefix(g.Repo, s.Name)
			mark := " "
			if i == m.selectedWorkspace && si == m.selectedSession {
				if m.previewErr {
					mark = "!"
				} else {
					mark = "."
				}
			}
			sLine := fmt.Sprintf("  %s %s %s", mark, att, name)
			if i == m.selectedWorkspace && si == m.selectedSession {
				lines = append(lines, sessSel.Render(sLine))
			} else {
				lines = append(lines, sessNorm.Render(sLine))
			}
		}

		if i != len(m.groups)-1 {
			lines = append(lines, "")
		}
	}

	if height > 0 {
		// Account for border + padding (top/bottom): 2 + 2
		maxLines := max(1, height-4)
		if len(lines) > maxLines {
			if maxLines == 1 {
				lines = []string{"..."}
			} else {
				lines = append(lines[:maxLines-1], "...")
			}
		}
	}

	return box.Render(strings.Join(lines, "\n"))
}

func trimRepoPrefix(repo, session string) string {
	r := strings.TrimSpace(repo)
	s := strings.TrimSpace(session)
	if r == "" || s == "" {
		return session
	}
	prefix := r + "-"
	if strings.HasPrefix(s, prefix) && len(s) > len(prefix) {
		return s[len(prefix):]
	}
	return session
}

func defaultSessionIndex(sessions []sessionInfo) int {
	if len(sessions) == 0 {
		return -1
	}
	return 0
}

func buildMenuItems(m model) []menuItem {
	items := []menuItem{}
	if _, ok := m.selectedSessionInfo(); ok {
		items = append(items, menuItem{Label: "Attach selected", Key: "attach"})
		items = append(items, menuItem{Label: "Destroy selected", Key: "destroy"})
	}
	items = append(items, menuItem{Label: "Refresh", Key: "refresh"})
	items = append(items, menuItem{Label: "Update", Key: "update"})
	items = append(items, menuItem{Label: "Quit", Key: "quit"})
	return items
}

func spawnAndAttachCmd(m model, commandName, command string) tea.Cmd {
	path := m.newSessionPath()
	repo := m.newSessionRepo()
	return func() tea.Msg {
		name, err := buildSessionName(repo, commandName)
		if err != nil {
			return viewCreatedMsg{err: err}
		}
		args := []string{"new-session", "-d", "-s", name}
		if strings.TrimSpace(path) != "" {
			args = append(args, "-c", path)
		}
		if _, err := runTmuxOut(args...); err != nil {
			return viewCreatedMsg{err: err}
		}
		if strings.TrimSpace(command) != "" {
			if _, err := runTmuxOut("send-keys", "-t", name+":0.0", command, "C-m"); err != nil {
				return viewCreatedMsg{err: err}
			}
		}
		return viewCreatedMsg{name: name, count: 1}
	}
}

func (m model) currentWorkspaceName() string {
	if len(m.groups) == 0 || m.selectedWorkspace < 0 || m.selectedWorkspace >= len(m.groups) {
		return "root"
	}
	return groupWorkspaceName(m.groups[m.selectedWorkspace])
}

func (m model) workspaceList() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, g := range m.groups {
		ws := groupWorkspaceName(g)
		if ws == "" {
			ws = "root"
		}
		if !seen[ws] {
			seen[ws] = true
			out = append(out, ws)
		}
	}
	if len(out) == 0 {
		return []string{"root"}
	}
	return out
}

func (m model) workspaceTotalSessions(workspace string) int {
	ws := strings.TrimSpace(workspace)
	if ws == "" {
		ws = "root"
	}
	total := 0
	for _, g := range m.groups {
		if groupWorkspaceName(g) != ws {
			continue
		}
		total += len(g.Sessions)
	}
	return total
}

func (m model) repoIndexesForWorkspace(workspace string) []int {
	ws := strings.TrimSpace(workspace)
	if ws == "" {
		ws = "root"
	}
	idxs := make([]int, 0, len(m.groups))
	for i, g := range m.groups {
		if groupWorkspaceName(g) == ws {
			idxs = append(idxs, i)
		}
	}
	return idxs
}

func (m model) repoOrder() []int {
	if len(m.groups) == 0 {
		return nil
	}
	out := make([]int, 0, len(m.groups))
	for i := range m.groups {
		out = append(out, i)
	}
	return out
}

func (m *model) shiftRepo(direction int) bool {
	order := m.repoOrder()
	if len(order) == 0 {
		return false
	}
	curPos := 0
	for i, idx := range order {
		if idx == m.selectedWorkspace {
			curPos = i
			break
		}
	}
	next := (curPos + direction + len(order)) % len(order)
	m.selectedWorkspace = order[next]
	m.selectedSession = defaultSessionIndex(m.groups[m.selectedWorkspace].Sessions)
	m.captureActive()
	return true
}

func (m *model) shiftSession(direction int) bool {
	order := m.repoOrder()
	if len(order) == 0 {
		return false
	}

	// Find current repo position in order.
	curRepoPos := 0
	for i, idx := range order {
		if idx == m.selectedWorkspace {
			curRepoPos = i
			break
		}
	}

	// Fast path: within current repo sessions.
	curSessions := m.currentSessions()
	if m.selectedSession < 0 {
		if len(curSessions) == 0 {
			// No sessions here; fall through to find another repo.
		} else {
			if direction > 0 {
				m.selectedSession = 0
			} else {
				m.selectedSession = len(curSessions) - 1
			}
			m.captureActive()
			return true
		}
	}
	if m.selectedSession >= 0 && m.selectedSession < len(curSessions) {
		nextSess := m.selectedSession + direction
		if nextSess >= 0 && nextSess < len(curSessions) {
			m.selectedSession = nextSess
			m.captureActive()
			return true
		}
	}

	// Boundary: move to next/prev repo that has sessions.
	pos := curRepoPos
	for step := 0; step < len(order); step++ {
		pos = (pos + direction + len(order)) % len(order)
		repoIdx := order[pos]
		sessions := m.groups[repoIdx].Sessions
		if len(sessions) == 0 {
			continue
		}
		m.selectedWorkspace = repoIdx
		if direction > 0 {
			m.selectedSession = 0
		} else {
			m.selectedSession = len(sessions) - 1
		}
		m.captureActive()
		return true
	}

	return false
}

func (m *model) shiftVertical(direction int) bool {
	order := m.repoOrder()
	if len(order) == 0 {
		return false
	}

	type pos struct {
		repo int
		sess int
	}

	positions := make([]pos, 0, len(order)*2)
	for _, repoIdx := range order {
		positions = append(positions, pos{repo: repoIdx, sess: -1})
		for si := range m.groups[repoIdx].Sessions {
			positions = append(positions, pos{repo: repoIdx, sess: si})
		}
	}
	if len(positions) == 0 {
		return false
	}

	cur := 0
	for i, p := range positions {
		if p.repo == m.selectedWorkspace && p.sess == m.selectedSession {
			cur = i
			break
		}
	}

	next := (cur + direction + len(positions)) % len(positions)
	m.selectedWorkspace = positions[next].repo
	m.selectedSession = positions[next].sess
	m.captureActive()
	return true
}

func groupWorkspaceName(g workspaceGroup) string {
	ws := strings.TrimSpace(g.Workspace)
	if ws == "" {
		return "root"
	}
	return ws
}

func (m model) renderSessions(width int) string {
	box := lipgloss.NewStyle().Width(width).Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)
	previewRaw := m.previewText
	if strings.TrimSpace(previewRaw) == "" {
		previewRaw = "(select a session)"
	}
	previewLines := strings.Split(previewRaw, "\n")
	if len(previewLines) == 1 && strings.TrimSpace(previewLines[0]) == "" {
		previewLines = []string{"(select a session)"}
	}
	if len(previewLines) > maxPreviewLines {
		previewLines = previewLines[len(previewLines)-maxPreviewLines:]
		previewLines[0] = "..."
	}
	for len(previewLines) < maxPreviewLines {
		previewLines = append(previewLines, "")
	}
	previewBody := strings.Join(previewLines, "\n")
	previewHeader := lipgloss.NewStyle().Foreground(lipgloss.Color("249")).Background(lipgloss.Color("236")).Padding(0, 1).Render("● ● ●  tmux preview")
	previewPane := lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("238")).Padding(0, 1).Render(previewBody)
	previewBox := lipgloss.JoinVertical(lipgloss.Left, previewHeader, previewPane)

	return box.Render(previewBox)
}

func (m *model) restoreSelection() {
	if len(m.groups) == 0 {
		m.selectedWorkspace = 0
		m.selectedSession = 0
		return
	}

	if m.activeWorkspace != "" {
		for i, g := range m.groups {
			if g.Name == m.activeWorkspace {
				m.selectedWorkspace = i
				break
			}
		}
	}

	if m.activeWorkspace == "" && strings.TrimSpace(m.preferredWorkspace) != "" {
		for i, g := range m.groups {
			if groupWorkspaceName(g) == strings.TrimSpace(m.preferredWorkspace) {
				m.selectedWorkspace = i
				break
			}
		}
	}

	if m.selectedWorkspace < 0 {
		m.selectedWorkspace = 0
	}
	if m.selectedWorkspace >= len(m.groups) {
		m.selectedWorkspace = len(m.groups) - 1
	}

	cur := m.currentSessions()
	if len(cur) == 0 {
		m.selectedSession = -1
		m.captureActive()
		return
	}

	if m.activeSession != "" {
		for i, s := range cur {
			if s.Name == m.activeSession {
				m.selectedSession = i
				break
			}
		}
	}

	if m.selectedSession < 0 {
		m.selectedSession = 0
	}
	if m.selectedSession >= len(cur) {
		m.selectedSession = len(cur) - 1
	}
	m.captureActive()
}

func (m *model) captureActive() {
	if len(m.groups) == 0 || m.selectedWorkspace < 0 || m.selectedWorkspace >= len(m.groups) {
		m.activeWorkspace = ""
		m.activeSession = ""
		return
	}
	ws := strings.TrimSpace(m.groups[m.selectedWorkspace].Workspace)
	if ws == "" {
		ws = "root"
	}
	if ws != m.preferredWorkspace {
		m.preferredWorkspace = ws
		_ = rememberWorkspaceTarget(remoteTarget(), ws)
	}
	m.activeWorkspace = m.groups[m.selectedWorkspace].Name
	cur := m.currentSessions()
	if len(cur) == 0 || m.selectedSession < 0 || m.selectedSession >= len(cur) {
		m.activeSession = ""
		return
	}
	m.activeSession = cur[m.selectedSession].Name
}

func (m model) currentSessions() []sessionInfo {
	if len(m.groups) == 0 || m.selectedWorkspace < 0 || m.selectedWorkspace >= len(m.groups) {
		return nil
	}
	return m.groups[m.selectedWorkspace].Sessions
}

func (m model) selectedSessionInfo() (sessionInfo, bool) {
	cur := m.currentSessions()
	if len(cur) == 0 || m.selectedSession < 0 || m.selectedSession >= len(cur) {
		return sessionInfo{}, false
	}
	return cur[m.selectedSession], true
}

func loadCmd() tea.Cmd {
	return func() tea.Msg {
		groups, err := groupedSessions()
		return loadedMsg{groups: groups, err: err}
	}
}

func newSessionCmd(path, repo, commandName, command string) tea.Cmd {
	return func() tea.Msg {
		name, err := buildSessionName(repo, commandName)
		if err != nil {
			return createdMsg{err: err}
		}
		args := []string{"new-session", "-d", "-s", name}
		if strings.TrimSpace(path) != "" {
			args = append(args, "-c", path)
		}
		_, err = runTmuxOut(args...)
		if err != nil {
			return createdMsg{err: err}
		}
		if strings.TrimSpace(command) != "" {
			_, err = runTmuxOut("send-keys", "-t", name+":0.0", command, "C-m")
			if err != nil {
				return createdMsg{err: err}
			}
		}
		return createdMsg{name: name, status: "Created " + name}
	}
}

func defaultSessionTemplates() []sessionTemplate {
	return []sessionTemplate{
		{Label: "Shell (default)", Name: "shell", Command: ""},
		{Label: "Claude (claude)", Name: "claude", Command: "claude"},
		{Label: "Claude FULL (sandbox off)", Name: "claude-full", Command: "IS_SANDBOX=1 claude --dangerously-skip-permissions"},
		{Label: "OpenCode (opencode)", Name: "opencode", Command: "opencode"},
		{Label: "Lazygit (lazygit)", Name: "lazygit", Command: "lazygit"},
	}
}

func buildSessionName(repo, commandName string) (string, error) {
	repoToken := sanitizeSessionToken(repo)
	if repoToken == "" {
		repoToken = "repo"
	}
	cmdToken := sanitizeSessionToken(commandName)
	if cmdToken == "" {
		cmdToken = "shell"
	}
	if len(repoToken) > 24 {
		repoToken = repoToken[:24]
	}
	if len(cmdToken) > 12 {
		cmdToken = cmdToken[:12]
	}
	prefix := repoToken + "-" + cmdToken + "-"
	n, err := nextSessionNumber(prefix)
	if err != nil {
		return "", err
	}
	return prefix + fmt.Sprintf("%d", n), nil
}

func nextSessionNumber(prefix string) (int, error) {
	metaOut, err := runTmuxOut("list-sessions", "-F", "#{session_name}")
	if err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "no server running") || strings.Contains(msg, "failed to connect") {
			return 1, nil
		}
		return 0, err
	}
	maxNum := 0
	for _, line := range strings.Split(strings.TrimSpace(metaOut), "\n") {
		sn := strings.TrimSpace(line)
		if !strings.HasPrefix(sn, prefix) {
			continue
		}
		n := atoiSafe(strings.TrimPrefix(sn, prefix))
		if n > maxNum {
			maxNum = n
		}
	}
	return maxNum + 1, nil
}

func sanitizeSessionToken(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	return out
}

func killSessionCmd(name string) tea.Cmd {
	return func() tea.Msg {
		_, err := runTmuxOut("kill-session", "-t", name)
		if err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{status: "Destroyed " + name}
	}
}

func (m model) newSessionPath() string {
	if len(m.groups) > 0 && m.selectedWorkspace >= 0 && m.selectedWorkspace < len(m.groups) {
		if p := strings.TrimSpace(m.groups[m.selectedWorkspace].Path); p != "" {
			return p
		}
	}
	if sel, ok := m.selectedSessionInfo(); ok && strings.TrimSpace(sel.Workdir) != "" {
		return sel.Workdir
	}
	return "/"
}

func (m model) newSessionRepo() string {
	if len(m.groups) > 0 && m.selectedWorkspace >= 0 && m.selectedWorkspace < len(m.groups) {
		repo := strings.TrimSpace(m.groups[m.selectedWorkspace].Repo)
		if repo != "" {
			return repo
		}
	}
	if sel, ok := m.selectedSessionInfo(); ok {
		if p := strings.TrimSpace(sel.Workdir); p != "" {
			return filepath.Base(p)
		}
	}
	return "sh"
}

func previewCmdForSelection(m model) tea.Cmd {
	sel, ok := m.selectedSessionInfo()
	if !ok {
		if len(m.groups) == 0 || m.selectedWorkspace < 0 || m.selectedWorkspace >= len(m.groups) {
			return nil
		}
		sessions := m.groups[m.selectedWorkspace].Sessions
		if len(sessions) == 0 {
			return nil
		}
		sel = sessions[0]
	}
	if m.previewPane != "" && m.previewSession == sel.Name {
		return nil
	}
	if splitTarget, ok := detectSoftAttachTarget(); ok {
		return softAttachPreviewCmd(m.previewPane, splitTarget, sel.Name)
	}
	return softAttachPreviewCmd(m.previewPane, "", sel.Name)
}

func (m model) softAttachPreviewEnabled() bool {
	return strings.TrimSpace(m.previewPane) != ""
}

func softAttachPreviewCmd(currentPane, splitTarget, session string) tea.Cmd {
	return func() tea.Msg {
		pane, err := ensureSoftPreviewPane(currentPane, splitTarget, session)
		return softAttachMsg{pane: pane, session: session, err: err}
	}
}

func ensureSoftPreviewPane(currentPane, splitTarget, session string) (string, error) {
	cmd := softAttachPaneCommand(session)
	pane := strings.TrimSpace(currentPane)
	if pane != "" {
		if _, err := runOut("tmux", "display-message", "-p", "-t", pane, "#{pane_id}"); err == nil {
			_, err = runOut("tmux", "respawn-pane", "-k", "-t", pane, cmd)
			if err != nil {
				return "", err
			}
			return pane, nil
		}
	}

	args := []string{"split-window", "-h", "-p", "75", "-d", "-P", "-F", "#{pane_id}"}
	if strings.TrimSpace(splitTarget) != "" {
		args = append(args, "-t", strings.TrimSpace(splitTarget))
	}
	args = append(args, cmd)
	out, err := runOut("tmux", args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func softAttachPaneCommand(session string) string {
	if isLocalRemote() {
		return "TMUX= tmux attach-session -t " + shellQuote(session)
	}
	args := []string{"mosh", remoteTarget(), "--", "tmux", "attach-session", "-t", session}
	return shellJoin(args)
}

func detectSoftAttachTarget() (string, bool) {
	if strings.TrimSpace(os.Getenv("TMUX")) != "" {
		out, err := runOut("tmux", "display-message", "-p", "#{pane_id}")
		if err == nil {
			pane := strings.TrimSpace(out)
			if pane != "" {
				return pane, true
			}
		}
		return "", true
	}

	tty, err := os.Readlink("/proc/self/fd/0")
	if err == nil {
		tty = strings.TrimSpace(tty)
		if tty != "" {
			out, derr := runOut("tmux", "display-message", "-p", "-t", tty, "#{pane_id}")
			if derr == nil {
				pane := strings.TrimSpace(out)
				if pane != "" {
					return pane, true
				}
			}
		}
	}

	out, err := runOut("tmux", "list-panes", "-a", "-F", "#{pane_id}")
	if err != nil {
		return "", false
	}
	for _, ln := range strings.Split(out, "\n") {
		v := strings.TrimSpace(ln)
		if v != "" {
			return v, true
		}
	}
	return "", false
}

func cleanupSoftPreview(m *model) {
	cleanupSoftPreviewPane(m.previewPane)
	m.previewPane = ""
	m.previewSession = ""
}

func cleanupSoftPreviewPane(pane string) {
	pane = strings.TrimSpace(pane)
	if pane == "" {
		return
	}
	_, _ = runOut("tmux", "kill-pane", "-t", pane)
}

func (m model) canFocusSoftAttach() bool {
	return strings.TrimSpace(m.previewPane) != "" && strings.TrimSpace(os.Getenv("TMUX")) != ""
}

func (m model) focusSoftAttach() {
	if !m.canFocusSoftAttach() {
		return
	}
	_, _ = runOut("tmux", "select-pane", "-t", m.previewPane)
}

func loadPreviewCmd(session string) tea.Cmd {
	return func() tea.Msg {
		text, err := capturePreview(session)
		return previewMsg{session: session, text: text, err: err}
	}
}

func capturePreview(session string) (string, error) {
	// capture-pane targets a pane; use the first pane of the first window by default.
	// Use -J to join wrapped lines for cleaner rendering in this fixed preview area.
	// Fallback to the session target for older tmux/edge cases.
	pane := session + ":0.0"
	out, err := runTmuxOut("capture-pane", "-p", "-J", "-t", pane)
	if err != nil {
		out2, err2 := runTmuxOut("capture-pane", "-p", "-J", "-t", session)
		if err2 != nil {
			return "", err
		}
		out = out2
	}
	return cleanPreview(out), nil
}

func cleanPreview(out string) string {
	out = strings.ReplaceAll(out, "\r", "")
	out = ansiRE.ReplaceAllString(out, "")
	return strings.Trim(out, "\n")
}

func tickCmd() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func attachCmd(session string) tea.Cmd {
	return tea.ExecProcess(tmuxAttachCmd(session), func(err error) tea.Msg {
		if err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{status: "Detached from " + session}
	})
}

func attachSessionNow(session string) error {
	cmd := tmuxAttachCmd(session)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func cycleRemoteCmd() tea.Cmd {
	return func() tea.Msg {
		targets, err := loadAllTargets()
		if err != nil || len(targets) == 0 {
			targets = []string{"local"}
		}

		// Find current target in list
		current := remoteTarget()
		nextIdx := 0
		for i, t := range targets {
			if t == current {
				nextIdx = (i + 1) % len(targets)
				break
			}
		}

		return remoteMsg{target: targets[nextIdx]}
	}
}

func loadAllTargets() ([]string, error) {
	path, err := targetsPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return []string{"local"}, nil
	}

	targets := []string{}
	seen := make(map[string]bool)
	for _, ln := range strings.Split(string(raw), "\n") {
		v := strings.TrimSpace(ln)
		if v != "" && !seen[v] {
			targets = append(targets, v)
			seen[v] = true
		}
	}

	// Always ensure "local" is in the list
	if !seen["local"] {
		targets = append(targets, "local")
	}

	return targets, nil
}

func groupedSessions() ([]workspaceGroup, error) {
	groups, err := discoverRepoGroupsCached()
	if err != nil {
		return nil, err
	}

	metaOut, err := runTmuxOut("list-sessions", "-F", "#{session_name}|#{session_attached}|#{session_windows}")
	if err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "no server running") || strings.Contains(msg, "failed to connect") {
			return groups, nil
		}
		return nil, err
	}
	metaOut = strings.TrimSpace(metaOut)
	if metaOut == "" {
		return groups, nil
	}

	pathOut, _ := runTmuxOut("list-panes", "-a", "-F", "#{session_name}|#{pane_index}|#{pane_current_path}")
	pathBySession := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(pathOut), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			continue
		}
		if strings.TrimSpace(parts[1]) != "0" {
			continue
		}
		sn := strings.TrimSpace(parts[0])
		if sn == "" {
			continue
		}
		if _, ok := pathBySession[sn]; !ok {
			pathBySession[sn] = strings.TrimSpace(parts[2])
		}
	}

	if len(groups) == 0 {
		groups = []workspaceGroup{{Workspace: "root", Repo: "root", Name: "root", Path: "/", Sessions: nil}}
	}

	for _, line := range strings.Split(metaOut, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(parts) != 3 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		if name == "" {
			continue
		}
		workdir := strings.TrimSpace(pathBySession[name])
		sess := sessionInfo{
			Name:     name,
			Workdir:  workdir,
			Attached: strings.TrimSpace(parts[1]) == "1",
			Windows:  atoiSafe(strings.TrimSpace(parts[2])),
		}

		best := 0 // root fallback
		bestLen := 0
		for i := 1; i < len(groups); i++ {
			gp := strings.TrimSpace(groups[i].Path)
			if gp == "" || gp == "/" {
				continue
			}
			if hasPathPrefix(workdir, gp) && len(gp) > bestLen {
				best = i
				bestLen = len(gp)
			}
		}
		groups[best].Sessions = append(groups[best].Sessions, sess)
	}

	for i := range groups {
		sort.Slice(groups[i].Sessions, func(a, b int) bool {
			return groups[i].Sessions[a].Name < groups[i].Sessions[b].Name
		})
	}

	return groups, nil
}

func discoverRepoGroups() ([]workspaceGroup, error) {
	root := remoteGitRoot()
	groups := []workspaceGroup{{Workspace: "root", Repo: "root", Name: "root", Path: "/", Sessions: nil}}

	repos, err := remoteListDirNames(root)
	if err != nil {
		return groups, nil
	}
	for _, repo := range repos {
		groups = append(groups, workspaceGroup{
			Workspace: "git",
			Repo:      repo,
			Name:      "git/" + repo,
			Path:      filepath.Join(root, repo),
			Sessions:  nil,
		})
	}

	sort.Slice(groups[1:], func(i, j int) bool {
		return groups[1+i].Name < groups[1+j].Name
	})
	return groups, nil
}

func remoteGitRoot() string {
	if isLocalRemote() {
		home, err := os.UserHomeDir()
		if err == nil {
			home = strings.TrimSpace(home)
			if home != "" {
				return filepath.Join(home, "git")
			}
		}
		return "/root/git"
	}

	out, err := runSSHShOut(remoteTarget(), `printf %s "$HOME"`)
	if err != nil {
		return "/root/git"
	}
	home := strings.TrimSpace(out)
	if home == "" {
		return "/root/git"
	}
	return filepath.Join(home, "git")
}

func discoverRepoGroupsCached() ([]workspaceGroup, error) {
	target := remoteTarget()
	if groups, ok := repoGroupCache[target]; ok {
		out := make([]workspaceGroup, len(groups))
		copy(out, groups)
		for i := range out {
			out[i].Sessions = nil
		}
		return out, nil
	}

	groups, err := discoverRepoGroups()
	if err != nil {
		return nil, err
	}
	copyGroups := make([]workspaceGroup, len(groups))
	copy(copyGroups, groups)
	repoGroupCache[target] = copyGroups

	for i := range groups {
		groups[i].Sessions = nil
	}
	return groups, nil
}

func workspaceColor(name string) string {
	palette := []string{"81", "112", "178", "203", "75", "141", "214"}
	h := 0
	for _, r := range name {
		h += int(r)
	}
	return palette[h%len(palette)]
}

func repoColor(name string) string {
	palette := []string{"81", "112", "178", "203", "75", "141", "214", "219", "69"}
	h := 0
	for _, r := range name {
		h += int(r)
	}
	return palette[h%len(palette)]
}

func remoteListDirNames(path string) ([]string, error) {
	if isLocalRemote() {
		entries, err := os.ReadDir(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, err
		}
		out := []string{}
		for _, e := range entries {
			if e.IsDir() {
				out = append(out, e.Name())
			}
		}
		sort.Strings(out)
		return out, nil
	}

	cmd := "root=" + shellQuote(path) + "; [ -d \"$root\" ] || exit 0; for d in \"$root\"/*; do [ -d \"$d\" ] || continue; basename \"$d\"; done"
	out, err := runSSHShOut(remoteTarget(), cmd)
	if err != nil {
		return nil, err
	}
	lines := []string{}
	for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			lines = append(lines, ln)
		}
	}
	sort.Strings(lines)
	return lines, nil
}

func hasPathPrefix(path, prefix string) bool {
	p := filepath.Clean(strings.TrimSpace(path))
	pr := filepath.Clean(strings.TrimSpace(prefix))
	if p == "" || pr == "" {
		return false
	}
	if p == pr {
		return true
	}
	return strings.HasPrefix(p, pr+string(os.PathSeparator))
}

func updateCmd() tea.Cmd {
	return func() tea.Msg {
		if updateRepoDir == "" {
			return actionMsg{err: errors.New("update unavailable (no local git repo)")}
		}
		if _, err := exec.LookPath("git"); err != nil {
			return actionMsg{err: errors.New("git not found")}
		}
		if _, err := exec.LookPath("go"); err != nil {
			return actionMsg{err: errors.New("go not found")}
		}

		dirty, err := runOutInDir(updateRepoDir, 8*time.Second, "git", "status", "--porcelain")
		if err != nil {
			return actionMsg{err: err}
		}
		if strings.TrimSpace(dirty) != "" {
			return actionMsg{err: errors.New("working tree is dirty")}
		}

		if _, err := runOutInDir(updateRepoDir, 25*time.Second, "git", "pull", "--ff-only", "origin", "main"); err != nil {
			return actionMsg{err: err}
		}
		if _, err := runOutInDir(updateRepoDir, 90*time.Second, "go", "build", "-o", "echoshell", "."); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{status: "Updated from origin/main. Restart echoshell."}
	}
}

func detectRepoDir() string {
	if env := strings.TrimSpace(os.Getenv("ECHOSHELL_REPO_DIR")); env != "" {
		if root := findGitRoot(env); root != "" {
			return root
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		if root := findGitRoot(cwd); root != "" {
			return root
		}
	}
	if exe, err := os.Executable(); err == nil {
		exePath := exe
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exePath = resolved
		}
		if root := findGitRoot(filepath.Dir(exePath)); root != "" {
			return root
		}
	}
	return ""
}

func findGitRoot(dir string) string {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" {
		return ""
	}
	for {
		if hasGitDir(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func hasGitDir(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil {
		return false
	}
	return st.IsDir()
}

func remoteTarget() string {
	return defaultRemoteTarget
}

func isLocalRemote() bool {
	r := strings.TrimSpace(strings.ToLower(remoteTarget()))
	return r == "" || r == "local"
}

func runTmuxOut(args ...string) (string, error) {
	if isLocalRemote() {
		return runOut("tmux", args...)
	}
	remoteCmd := "tmux " + shellJoin(args)
	return runSSHShOut(remoteTarget(), remoteCmd)
}

func tmuxAttachCmd(session string) *exec.Cmd {
	if isLocalRemote() {
		return exec.Command("tmux", "attach-session", "-t", session)
	}
	return exec.Command("mosh", remoteTarget(), "--", "tmux", "attach-session", "-t", session)
}

func shouldUseMosh() bool {
	if isLocalRemote() {
		return false
	}
	if _, err := exec.LookPath("mosh"); err != nil {
		return false
	}
	return true
}

func sshControlPath() string {
	return filepath.Join(os.TempDir(), "echoshell-ssh-%C")
}

func sshBaseArgs(target string) []string {
	return []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=8",
		"-o", "ControlMaster=auto",
		"-o", "ControlPersist=120",
		"-o", "ControlPath=" + sshControlPath(),
		target,
	}
}

func sshAttachArgs(target string) []string {
	return []string{
		"-t",
		"-o", "ControlMaster=auto",
		"-o", "ControlPersist=120",
		"-o", "ControlPath=" + sshControlPath(),
		target,
	}
}

func runSSHShOut(target, command string) (string, error) {
	args := append(sshBaseArgs(target), "sh -lc "+shellQuote(command))
	return runOut("ssh", args...)
}

func resolveRemoteTarget() string {
	env := strings.TrimSpace(os.Getenv("ECHOSHELL_REMOTE"))
	if env != "" {
		return env
	}
	if saved, _ := loadLastRemoteTarget(); strings.TrimSpace(saved) != "" {
		return saved
	}
	return defaultRemoteTarget
}

func targetsPath() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "echoshell", "targets.txt"), nil
}

func loadLastRemoteTarget() (string, error) {
	path, err := targetsPath()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, ln := range strings.Split(string(raw), "\n") {
		v := strings.TrimSpace(ln)
		if v != "" {
			return v, nil
		}
	}
	return "", nil
}

func rememberRemoteTarget(target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil
	}
	path, err := targetsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	current := []string{}
	raw, err := os.ReadFile(path)
	if err == nil {
		for _, ln := range strings.Split(string(raw), "\n") {
			v := strings.TrimSpace(ln)
			if v != "" && v != target {
				current = append(current, v)
			}
		}
	}
	next := append([]string{target}, current...)
	if len(next) > 20 {
		next = next[:20]
	}
	return os.WriteFile(path, []byte(strings.Join(next, "\n")+"\n"), 0o644)
}

func workspacesPath() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "echoshell", "workspaces.txt"), nil
}

func normalizeTarget(target string) string {
	v := strings.TrimSpace(target)
	if v == "" {
		return defaultRemoteTarget
	}
	if strings.EqualFold(v, "localhost") || strings.EqualFold(v, "root@localhost") {
		return "local"
	}
	return v
}

func loadLastWorkspaceTarget(target string) (string, error) {
	target = normalizeTarget(target)
	path, err := workspacesPath()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, ln := range strings.Split(string(raw), "\n") {
		parts := strings.SplitN(strings.TrimSpace(ln), "|", 2)
		if len(parts) != 2 {
			continue
		}
		if normalizeTarget(parts[0]) == target {
			return strings.TrimSpace(parts[1]), nil
		}
	}
	return "", nil
}

func rememberWorkspaceTarget(target, workspace string) error {
	target = normalizeTarget(target)
	workspace = strings.TrimSpace(workspace)
	if target == "" || workspace == "" {
		return nil
	}
	path, err := workspacesPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	rows := []string{target + "|" + workspace}
	raw, err := os.ReadFile(path)
	if err == nil {
		for _, ln := range strings.Split(string(raw), "\n") {
			parts := strings.SplitN(strings.TrimSpace(ln), "|", 2)
			if len(parts) != 2 {
				continue
			}
			t := normalizeTarget(parts[0])
			w := strings.TrimSpace(parts[1])
			if t == "" || w == "" || t == target {
				continue
			}
			rows = append(rows, t+"|"+w)
		}
	}
	if len(rows) > 20 {
		rows = rows[:20]
	}
	return os.WriteFile(path, []byte(strings.Join(rows, "\n")+"\n"), 0o644)
}

func shellJoin(args []string) string {
	if len(args) == 0 {
		return ""
	}
	q := make([]string, 0, len(args))
	for _, a := range args {
		q = append(q, shellQuote(a))
	}
	return strings.Join(q, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n'\"`$&|;<>*?[]{}()!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func runOut(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%s timed out", name)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	return stdout.String(), nil
}

func runOutInDir(dir string, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%s timed out", name)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	return stdout.String(), nil
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
