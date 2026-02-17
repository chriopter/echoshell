package main

import (
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
const workspaceAll = "all"

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

type previewMsg struct {
	session string
	text    string
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

type model struct {
	width              int
	height             int
	groups             []workspaceGroup
	selectedWorkspace  int
	selectedSession    int
	preferredWorkspace string
	workspaceView      string
	activeWorkspace    string
	activeSession      string
	previewSession     string
	previewText        string
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
	if _, err := exec.LookPath("ssh"); err != nil {
		return errors.New("ssh is required")
	}

	lastTarget := resolveRemoteTarget()
	selectedRemoteTarget = strings.TrimSpace(lastTarget)

	targets, selectedIdx := loadTargetsForSelection(lastTarget)

	updateRepoDir = detectRepoDir()
	preferredWorkspace, _ := loadLastWorkspaceTarget(selectedRemoteTarget)
	workspaceView := workspaceAll
	if strings.TrimSpace(preferredWorkspace) != "" {
		workspaceView = preferredWorkspace
	}

	m := model{
		status:             "Loading sessions...",
		selectingRemote:    false,
		availableTargets:   targets,
		selectedTarget:     selectedIdx,
		preferredWorkspace: preferredWorkspace,
		workspaceView:      workspaceView,
		newTemplates:       defaultSessionTemplates(),
	}

	if os.Getenv("ECHOSHELL_SELECT_REMOTE") == "1" {
		m.selectingRemote = true
		m.status = "Select remote target..."
	}

	if isLocalRemote() {
		if _, err := exec.LookPath("tmux"); err != nil {
			return errors.New("tmux is required for local mode")
		}
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
						m.workspaceView = workspaceAll
						if strings.TrimSpace(m.preferredWorkspace) != "" {
							m.workspaceView = m.preferredWorkspace
						}
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
				m.workspaceView = workspaceAll
				if strings.TrimSpace(m.preferredWorkspace) != "" {
					m.workspaceView = m.preferredWorkspace
				}
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
					return m, tea.Quit
				}
				name := m.quickCandidates[m.selectedQuick].Session.Name
				m.status = "Attaching " + name + "..."
				return m, attachCmd(name)
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

	case previewMsg:
		if msg.err != nil {
			m.previewSession = msg.session
			m.previewText = "Preview error: " + msg.err.Error()
			return m, nil
		}
		m.previewSession = msg.session
		m.previewText = strings.TrimRight(msg.text, "\n")
		if strings.TrimSpace(m.previewText) == "" {
			m.previewText = "(no output yet)"
		}
		return m, nil

	case remoteMsg:
		if msg.err != nil {
			m.status = "Remote change failed: " + msg.err.Error()
			return m, nil
		}
		selectedRemoteTarget = msg.target
		_ = rememberRemoteTarget(selectedRemoteTarget)
		m.preferredWorkspace, _ = loadLastWorkspaceTarget(selectedRemoteTarget)
		m.workspaceView = workspaceAll
		if strings.TrimSpace(m.preferredWorkspace) != "" {
			m.workspaceView = m.preferredWorkspace
		}
		m.activeWorkspace = ""
		m.activeSession = ""
		m.status = "Switched to remote: " + remoteTarget()
		return m, loadCmd()

	case tea.KeyMsg:
		switch strings.ToLower(msg.String()) {
		case "ctrl+c", "q", "esc":
			return m, tea.Quit
		case "left", "h":
			if m.shiftWorkspaceView(-1) {
				m.selectedSession = 0
				m.captureActive()
				return m, previewCmdForSelection(m)
			}
			return m, nil
		case "right", "l":
			if m.shiftWorkspaceView(1) {
				m.selectedSession = 0
				m.captureActive()
				return m, previewCmdForSelection(m)
			}
			return m, nil
		case "tab":
			if m.shiftWorkspaceView(1) {
				m.selectedSession = 0
				m.captureActive()
				return m, previewCmdForSelection(m)
			}
			return m, nil
		case "shift+tab":
			if m.shiftWorkspaceView(-1) {
				m.selectedSession = 0
				m.captureActive()
				return m, previewCmdForSelection(m)
			}
			return m, nil
		case "up", "k":
			repos := m.repoIndexesForView()
			cur := -1
			for i, idx := range repos {
				if idx == m.selectedWorkspace {
					cur = i
					break
				}
			}
			if cur > 0 {
				m.selectedWorkspace = repos[cur-1]
				m.selectedSession = 0
				m.captureActive()
				return m, previewCmdForSelection(m)
			}
			return m, nil
		case "down", "j":
			repos := m.repoIndexesForView()
			cur := -1
			for i, idx := range repos {
				if idx == m.selectedWorkspace {
					cur = i
					break
				}
			}
			if cur >= 0 && cur < len(repos)-1 {
				m.selectedWorkspace = repos[cur+1]
				m.selectedSession = 0
				m.captureActive()
				return m, previewCmdForSelection(m)
			}
			return m, nil
		case "r":
			m.status = "Refreshing..."
			return m, loadCmd()
		case "t":
			last := remoteTarget()
			m.availableTargets, m.selectedTarget = loadTargetsForSelection(last)
			m.selectingRemote = true
			m.addingNewRemote = false
			m.newRemoteInput = ""
			m.status = "Select remote target..."
			return m, nil
		case "u":
			if m.updateBusy {
				return m, nil
			}
			m.updateBusy = true
			m.status = "Updating from origin/main..."
			return m, updateCmd()
		case "n":
			m.selectingNew = true
			m.selectedTemplate = 0
			m.status = "Pick command for new session"
			return m, nil
		case "d":
			sel, ok := m.selectedSessionInfo()
			if !ok {
				return m, nil
			}
			m.status = "Destroying " + sel.Name + "..."
			return m, killSessionCmd(sel.Name)
		case "enter":
			sel, ok := m.selectedSessionInfo()
			if !ok {
				return m, nil
			}
			m.status = "Attaching " + sel.Name + "..."
			return m, attachCmd(sel.Name)
		case "1", "2", "3", "4":
			s := strings.ToLower(msg.String())
			idx := int(s[0] - '1')
			if idx >= 0 && idx < len(m.currentSessions()) {
				m.selectedSession = idx
				m.captureActive()
				return m, previewCmdForSelection(m)
			}
			return m, nil
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
	helpNav := lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Render("tab/s-tab workspace  j/k repo  1-4 session  enter attach")
	helpActions := lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render("n new  d destroy  t target  r refresh  u update  q quit")
	help := lipgloss.JoinVertical(lipgloss.Left, helpNav, helpActions)
	status := lipgloss.NewStyle().Foreground(lipgloss.Color("111")).Render("status: " + m.status)

	if len(m.groups) == 0 {
		empty := lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Padding(1, 2).Render("No sessions")
		return lipgloss.JoinVertical(lipgloss.Left, title, remote, empty, status, help)
	}

	leftW := 30
	if m.width > 0 {
		leftW = max(24, min(36, m.width/4))
	}
	rightW := max(50, m.width-leftW-6)

	left := m.renderWorkspaces(leftW)
	right := m.renderSessions(rightW)
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)

	return lipgloss.JoinVertical(lipgloss.Left, title, remote, body, status, help)
}

func (m model) renderWorkspaces(width int) string {
	box := lipgloss.NewStyle().Width(width).Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)
	view := m.workspaceViewName()
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220")).Render("Repo " + view)
	sel := lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62")).Padding(0, 1)
	norm := lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Padding(0, 1)

	lines := []string{title, ""}
	if view == workspaceAll {
		heading := lipgloss.NewStyle().Bold(true)
		lastWorkspace := ""
		for _, i := range m.repoIndexesForView() {
			g := m.groups[i]
			ws := groupWorkspaceName(g)
			if ws != lastWorkspace {
				lastWorkspace = ws
				lines = append(lines, heading.Foreground(lipgloss.Color(workspaceColor(ws))).Render("Workspace "+ws))
			}
			line := fmt.Sprintf("  %s (%d)", g.Repo, len(g.Sessions))
			if i == m.selectedWorkspace {
				lines = append(lines, sel.Render(line))
			} else {
				lines = append(lines, norm.Render(line))
			}
		}
	} else {
		wsHeading := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(workspaceColor(view))).Render("Workspace " + view)
		lines = append(lines, wsHeading, "")
		for _, i := range m.repoIndexesForView() {
			g := m.groups[i]
			line := fmt.Sprintf("  %s (%d)", g.Repo, len(g.Sessions))
			if i == m.selectedWorkspace {
				lines = append(lines, sel.Render(line))
			} else {
				lines = append(lines, norm.Render(line))
			}
		}
	}
	return box.Render(strings.Join(lines, "\n"))
}

func (m model) currentWorkspaceName() string {
	if len(m.groups) == 0 || m.selectedWorkspace < 0 || m.selectedWorkspace >= len(m.groups) {
		return "root"
	}
	return groupWorkspaceName(m.groups[m.selectedWorkspace])
}

func (m model) workspaceViewName() string {
	v := strings.TrimSpace(m.workspaceView)
	if v == "" {
		return workspaceAll
	}
	return v
}

func (m model) repoIndexesForView() []int {
	view := m.workspaceViewName()
	idxs := make([]int, 0, len(m.groups))
	for i, g := range m.groups {
		if view == workspaceAll || groupWorkspaceName(g) == view {
			idxs = append(idxs, i)
		}
	}
	return idxs
}

func (m model) workspaceOrder() []string {
	order := []string{workspaceAll}
	seen := map[string]bool{workspaceAll: true}
	for _, g := range m.groups {
		ws := groupWorkspaceName(g)
		if !seen[ws] {
			order = append(order, ws)
			seen[ws] = true
		}
	}
	return order
}

func (m *model) shiftWorkspaceView(direction int) bool {
	if len(m.groups) == 0 {
		return false
	}
	order := m.workspaceOrder()
	if len(order) == 0 {
		return false
	}
	curView := m.workspaceViewName()
	curIdx := 0
	for i, ws := range order {
		if ws == curView {
			curIdx = i
			break
		}
	}
	next := (curIdx + direction + len(order)) % len(order)
	want := order[next]
	m.workspaceView = want

	if want == workspaceAll {
		if m.selectedWorkspace < 0 || m.selectedWorkspace >= len(m.groups) {
			m.selectedWorkspace = 0
		}
		return true
	}

	for i, g := range m.groups {
		if groupWorkspaceName(g) == want {
			m.selectedWorkspace = i
			return true
		}
	}
	return false
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
	g := m.groups[m.selectedWorkspace]
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214")).Render("Sessions: " + g.Name)
	sel := lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62")).Padding(0, 1)
	norm := lipgloss.NewStyle().Padding(0, 1)

	lines := []string{title, ""}
	if len(g.Sessions) == 0 {
		lines = append(lines, "(no sessions)")
		return box.Render(strings.Join(lines, "\n"))
	}

	for i, s := range g.Sessions {
		att := " "
		if s.Attached {
			att = "*"
		}
		line := fmt.Sprintf("%s %s  win:%d", att, s.Name, s.Windows)
		if i == m.selectedSession {
			lines = append(lines, sel.Render(line))
		} else {
			lines = append(lines, norm.Render(line))
		}
	}

	if s, ok := m.selectedSessionInfo(); ok {
		lines = append(lines, "", "workdir: "+s.Workdir)
	}

	previewRaw := m.previewText
	if strings.TrimSpace(previewRaw) == "" {
		previewRaw = "(select a session)"
	}
	previewLines := strings.Split(previewRaw, "\n")
	if len(previewLines) == 1 && strings.TrimSpace(previewLines[0]) == "" {
		previewLines = []string{"(select a session)"}
	}
	if len(previewLines) > maxPreviewLines {
		previewLines = previewLines[:maxPreviewLines]
		previewLines = append(previewLines, "...")
	}
	for len(previewLines) < maxPreviewLines {
		previewLines = append(previewLines, "")
	}
	previewBody := strings.Join(previewLines, "\n")
	previewHeader := lipgloss.NewStyle().Foreground(lipgloss.Color("249")).Background(lipgloss.Color("236")).Padding(0, 1).Render("● ● ●  terminal preview")
	previewPane := lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("238")).Padding(0, 1).Render(previewBody)
	previewBox := lipgloss.JoinVertical(lipgloss.Left, previewHeader, previewPane)
	previewTitle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("45")).Render("Preview")
	lines = append(lines, "", previewTitle, previewBox)

	return box.Render(strings.Join(lines, "\n"))
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
			if g.Workspace == m.preferredWorkspace {
				m.selectedWorkspace = i
				break
			}
		}
	}

	if strings.TrimSpace(m.workspaceView) == "" {
		m.workspaceView = workspaceAll
		if strings.TrimSpace(m.preferredWorkspace) != "" {
			m.workspaceView = m.preferredWorkspace
		}
	}

	if m.selectedWorkspace < 0 {
		m.selectedWorkspace = 0
	}
	if m.selectedWorkspace >= len(m.groups) {
		m.selectedWorkspace = len(m.groups) - 1
	}

	if m.workspaceViewName() != workspaceAll && groupWorkspaceName(m.groups[m.selectedWorkspace]) != m.workspaceViewName() {
		for i, g := range m.groups {
			if groupWorkspaceName(g) == m.workspaceViewName() {
				m.selectedWorkspace = i
				break
			}
		}
	}

	cur := m.currentSessions()
	if len(cur) == 0 {
		m.selectedSession = 0
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
		{Label: "Claude FULL (IS_SANDBOX=1 claude --dangerously-skip-permissions)", Name: "claude-full", Command: "IS_SANDBOX=1 claude --dangerously-skip-permissions"},
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
		return nil
	}
	return loadPreviewCmd(sel.Name)
}

func loadPreviewCmd(session string) tea.Cmd {
	return func() tea.Msg {
		text, err := capturePreview(session)
		return previewMsg{session: session, text: text, err: err}
	}
}

func capturePreview(session string) (string, error) {
	out, err := runTmuxOut("capture-pane", "-a", "-p", "-J", "-N", "-S", "-80", "-t", session)
	if err != nil {
		return "", err
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
	const root = "/root/workspaces"
	groups := []workspaceGroup{{Workspace: "root", Repo: "root", Name: "root", Path: "/", Sessions: nil}}

	workspaces, err := remoteListDirNames(root)
	if err != nil {
		return groups, nil
	}
	for _, ws := range workspaces {
		repoRoot := filepath.Join(root, ws, "data", "repos")
		repos, rerr := remoteListDirNames(repoRoot)
		if rerr != nil {
			continue
		}
		for _, repo := range repos {
			groups = append(groups, workspaceGroup{
				Workspace: ws,
				Repo:      repo,
				Name:      ws + "/" + repo,
				Path:      filepath.Join(repoRoot, repo),
				Sessions:  nil,
			})
		}
	}

	sort.Slice(groups[1:], func(i, j int) bool {
		return groups[1+i].Name < groups[1+j].Name
	})
	return groups, nil
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
	if cwd, err := os.Getwd(); err == nil && hasGitDir(cwd) {
		return cwd
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if hasGitDir(dir) {
			return dir
		}
	}
	return ""
}

func hasGitDir(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil {
		return false
	}
	return st.IsDir()
}

func remoteTarget() string {
	v := strings.TrimSpace(selectedRemoteTarget)
	if v == "" {
		return defaultRemoteTarget
	}
	if strings.EqualFold(v, "localhost") || strings.EqualFold(v, "root@localhost") {
		return "local"
	}
	return v
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
	remoteCmd := "tmux attach-session -t " + shellQuote(session)
	args := append(sshAttachArgs(remoteTarget()), "sh -lc "+shellQuote(remoteCmd))
	return exec.Command("ssh", args...)
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
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%s timed out", name)
	}
	if err != nil {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func runOutInDir(dir string, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%s timed out", name)
	}
	if err != nil {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return string(out), nil
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
