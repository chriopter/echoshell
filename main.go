package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

type sessionInfo struct {
	Name     string
	Workdir  string
	Attached bool
	Windows  int
}

type workspaceGroup struct {
	Name     string
	Path     string
	Sessions []sessionInfo
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

type model struct {
	width             int
	height            int
	groups            []workspaceGroup
	selectedWorkspace int
	selectedSession   int
	activeWorkspace   string
	activeSession     string
	previewSession    string
	previewText       string
	updateBusy        bool
	status            string
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
	selectedRemoteTarget = resolveRemoteTarget()
	_ = rememberRemoteTarget(selectedRemoteTarget)
	updateRepoDir = detectRepoDir()
	if isLocalRemote() {
		if _, err := exec.LookPath("tmux"); err != nil {
			return errors.New("tmux is required for local mode")
		}
	}
	m := model{status: "Loading sessions..."}
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func (m model) Init() tea.Cmd {
	return tea.Batch(loadCmd(), tickCmd())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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

	case tea.KeyMsg:
		switch strings.ToLower(msg.String()) {
		case "ctrl+c", "q", "esc":
			return m, tea.Quit
		case "left", "h":
			if m.selectedWorkspace > 0 {
				m.selectedWorkspace--
				m.selectedSession = 0
				m.captureActive()
				return m, previewCmdForSelection(m)
			}
			return m, nil
		case "right", "l":
			if m.selectedWorkspace < len(m.groups)-1 {
				m.selectedWorkspace++
				m.selectedSession = 0
				m.captureActive()
				return m, previewCmdForSelection(m)
			}
			return m, nil
		case "tab":
			if len(m.groups) > 0 {
				m.selectedWorkspace = (m.selectedWorkspace + 1) % len(m.groups)
				m.selectedSession = 0
				m.captureActive()
				return m, previewCmdForSelection(m)
			}
			return m, nil
		case "up", "k":
			if m.selectedSession > 0 {
				m.selectedSession--
				m.captureActive()
				return m, previewCmdForSelection(m)
			}
			return m, nil
		case "down", "j":
			if m.selectedSession < len(m.currentSessions())-1 {
				m.selectedSession++
				m.captureActive()
				return m, previewCmdForSelection(m)
			}
			return m, nil
		case "r":
			m.status = "Refreshing..."
			return m, loadCmd()
		case "u":
			if m.updateBusy {
				return m, nil
			}
			m.updateBusy = true
			m.status = "Updating from origin/main..."
			return m, updateCmd()
		case "n":
			m.status = "Creating new session..."
			return m, newSessionCmd(m.newSessionPath())
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
	remote := lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Render("remote: " + remoteTarget())
	help := lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Render("tab repo  1..4 session  j/k session  n new  d destroy  u update  enter attach  r refresh  q quit")
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
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220")).Render("Repos")
	sel := lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62")).Padding(0, 1)
	norm := lipgloss.NewStyle().Padding(0, 1)

	lines := []string{title, ""}
	for i, g := range m.groups {
		line := fmt.Sprintf("%s (%d)", g.Name, len(g.Sessions))
		if i == m.selectedWorkspace {
			lines = append(lines, sel.Render(line))
		} else {
			lines = append(lines, norm.Render(line))
		}
	}
	return box.Render(strings.Join(lines, "\n"))
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

	lines = append(lines, "", "Preview:")
	previewLines := strings.Split(strings.TrimSpace(m.previewText), "\n")
	if len(previewLines) == 1 && strings.TrimSpace(previewLines[0]) == "" {
		previewLines = []string{"(select a session)"}
	}
	if len(previewLines) > maxPreviewLines {
		previewLines = previewLines[:maxPreviewLines]
		previewLines = append(previewLines, "...")
	}
	for _, pl := range previewLines {
		lines = append(lines, "  "+pl)
	}

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

	if m.selectedWorkspace < 0 {
		m.selectedWorkspace = 0
	}
	if m.selectedWorkspace >= len(m.groups) {
		m.selectedWorkspace = len(m.groups) - 1
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

func newSessionCmd(path string) tea.Cmd {
	return func() tea.Msg {
		name := "sh-" + time.Now().Format("150405")
		args := []string{"new-session", "-d", "-s", name}
		if strings.TrimSpace(path) != "" {
			args = append(args, "-c", path)
		}
		_, err := runTmuxOut(args...)
		if err != nil {
			return createdMsg{err: err}
		}
		return createdMsg{name: name, status: "Created " + name}
	}
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
	out, err := runTmuxOut("capture-pane", "-e", "-p", "-S", "-60", "-t", session)
	if err != nil {
		return "", err
	}
	return out, nil
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

func groupedSessions() ([]workspaceGroup, error) {
	groups, err := discoverRepoGroups()
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
		groups = []workspaceGroup{{Name: "root", Path: "/", Sessions: nil}}
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
	groups := []workspaceGroup{{Name: "root", Path: "/", Sessions: nil}}

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
				Name:     ws + "/" + repo,
				Path:     filepath.Join(repoRoot, repo),
				Sessions: nil,
			})
		}
	}

	sort.Slice(groups[1:], func(i, j int) bool {
		return groups[1+i].Name < groups[1+j].Name
	})
	return groups, nil
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
	out, err := runOut("ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=8", remoteTarget(), "sh -lc "+shellQuote(cmd))
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
	return runOut("ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=8", remoteTarget(), "sh -lc "+shellQuote(remoteCmd))
}

func tmuxAttachCmd(session string) *exec.Cmd {
	if isLocalRemote() {
		return exec.Command("tmux", "attach-session", "-t", session)
	}
	remoteCmd := "tmux attach-session -t " + shellQuote(session)
	return exec.Command("ssh", "-t", remoteTarget(), "sh -lc "+shellQuote(remoteCmd))
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
