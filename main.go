package main

import (
	"context"
	"encoding/json"
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

const (
	defaultSessionPrefix = "sh"
	refreshInterval      = 3 * time.Second
)

type screen int

const (
	screenServers screen = iota
	screenAddServerName
	screenAddServerConn
	screenSessions
)

type serverProfile struct {
	Name string `json:"name"`
	Conn string `json:"conn"`
}

type serverConfig struct {
	Servers []serverProfile `json:"servers"`
}

type sessionInfo struct {
	Name     string
	Attached bool
	Profile  string
}

type serversLoadedMsg struct {
	servers []serverProfile
	err     error
}

type sessionsLoadedMsg struct {
	sessions []sessionInfo
	err      error
}

type actionDoneMsg struct {
	status string
	err    error
}

type refreshTickMsg time.Time

type model struct {
	screen          screen
	status          string
	servers         []serverProfile
	selectedServer  int
	sessions        []sessionInfo
	selectedSession int
	connected       int

	input       string
	pendingName string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return errors.New("ssh is required but was not found in PATH")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		return errors.New("tmux is required but was not found in PATH")
	}

	m := model{
		screen:    screenServers,
		connected: -1,
		status:    "Load servers...",
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func (m model) Init() tea.Cmd {
	return loadServersCmd()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case serversLoadedMsg:
		if msg.err != nil {
			m.status = "Failed to load servers: " + msg.err.Error()
			return m, nil
		}
		m.servers = msg.servers
		if len(m.servers) == 0 {
			m.selectedServer = 0
			m.status = "No servers yet. Press a to add one."
		} else {
			if m.selectedServer >= len(m.servers) {
				m.selectedServer = len(m.servers) - 1
			}
			m.status = fmt.Sprintf("Loaded %d server(s)", len(m.servers))
		}
		return m, nil

	case sessionsLoadedMsg:
		if msg.err != nil {
			m.status = "Connection failed: " + msg.err.Error()
			return m, nil
		}
		m.sessions = msg.sessions
		if len(m.sessions) == 0 {
			m.selectedSession = 0
			m.status = "Connected. No tmux sessions yet. Press n."
		} else {
			if m.selectedSession >= len(m.sessions) {
				m.selectedSession = len(m.sessions) - 1
			}
			m.status = fmt.Sprintf("Connected. %d tmux session(s)", len(m.sessions))
		}
		return m, refreshTickCmd()

	case actionDoneMsg:
		if msg.err != nil {
			m.status = "Action failed: " + msg.err.Error()
			return m, nil
		}
		if msg.status != "" {
			m.status = msg.status
		}
		if m.screen == screenSessions {
			srv, ok := m.currentServer()
			if ok {
				return m, loadSessionsCmd(srv.Conn)
			}
		}
		return m, nil

	case refreshTickMsg:
		if m.screen != screenSessions {
			return m, nil
		}
		srv, ok := m.currentServer()
		if !ok {
			return m, nil
		}
		return m, tea.Batch(loadSessionsCmd(srv.Conn), refreshTickCmd())

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		switch m.screen {
		case screenServers:
			return updateServersScreen(m, msg)
		case screenAddServerName:
			return updateAddNameScreen(m, msg)
		case screenAddServerConn:
			return updateAddConnScreen(m, msg)
		case screenSessions:
			return updateSessionsScreen(m, msg)
		}
	}

	return m, nil
}

func updateServersScreen(m model, key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "q":
		return m, tea.Quit
	case "a":
		m.input = ""
		m.pendingName = ""
		m.screen = screenAddServerName
		m.status = "Enter server name"
		return m, nil
	case "up", "k":
		if m.selectedServer > 0 {
			m.selectedServer--
		}
		return m, nil
	case "down", "j":
		if m.selectedServer < len(m.servers)-1 {
			m.selectedServer++
		}
		return m, nil
	case "enter":
		srv, ok := m.selectedServerProfile()
		if !ok {
			m.status = "No server selected"
			return m, nil
		}
		m.connected = m.selectedServer
		m.sessions = nil
		m.selectedSession = 0
		m.screen = screenSessions
		m.status = "Connecting to " + srv.Name + " (" + srv.Conn + ")..."
		return m, loadSessionsCmd(srv.Conn)
	case "d":
		if len(m.servers) == 0 {
			return m, nil
		}
		name := m.servers[m.selectedServer].Name
		m.servers = append(m.servers[:m.selectedServer], m.servers[m.selectedServer+1:]...)
		if m.selectedServer >= len(m.servers) && len(m.servers) > 0 {
			m.selectedServer = len(m.servers) - 1
		}
		if len(m.servers) == 0 {
			m.selectedServer = 0
		}
		m.status = "Deleted server " + name
		return m, saveServersCmd(m.servers)
	}
	return m, nil
}

func updateAddNameScreen(m model, key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.screen = screenServers
		m.status = "Cancelled"
		return m, nil
	case "enter":
		name := strings.TrimSpace(m.input)
		if name == "" {
			m.status = "Name cannot be empty"
			return m, nil
		}
		m.pendingName = name
		m.input = ""
		m.screen = screenAddServerConn
		m.status = "Enter connection command (e.g. root@ip)"
		return m, nil
	case "backspace":
		if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
		return m, nil
	default:
		if key.Type == tea.KeyRunes {
			m.input += string(key.Runes)
		}
		return m, nil
	}
}

func updateAddConnScreen(m model, key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.screen = screenServers
		m.status = "Cancelled"
		m.input = ""
		m.pendingName = ""
		return m, nil
	case "enter":
		conn := strings.TrimSpace(m.input)
		if strings.EqualFold(conn, "lcoalhost") {
			conn = "root@localhost"
		}
		if conn == "" {
			m.status = "Connection cannot be empty"
			return m, nil
		}
		m.servers = upsertServer(m.servers, serverProfile{Name: m.pendingName, Conn: conn})
		sort.Slice(m.servers, func(i, j int) bool { return m.servers[i].Name < m.servers[j].Name })
		for i := range m.servers {
			if m.servers[i].Name == m.pendingName {
				m.selectedServer = i
				break
			}
		}
		m.screen = screenServers
		m.status = "Saved server " + m.pendingName
		m.input = ""
		m.pendingName = ""
		return m, saveServersCmd(m.servers)
	case "backspace":
		if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
		return m, nil
	default:
		if key.Type == tea.KeyRunes {
			m.input += string(key.Runes)
		}
		return m, nil
	}
}

func updateSessionsScreen(m model, key tea.KeyMsg) (tea.Model, tea.Cmd) {
	srv, ok := m.currentServer()
	if !ok {
		m.screen = screenServers
		m.status = "Server not found"
		return m, nil
	}

	switch key.String() {
	case "q":
		return m, tea.Quit
	case "b", "esc":
		m.screen = screenServers
		m.status = "Disconnected from " + srv.Name
		return m, nil
	case "up", "k":
		if m.selectedSession > 0 {
			m.selectedSession--
		}
		return m, nil
	case "down", "j":
		if m.selectedSession < len(m.sessions)-1 {
			m.selectedSession++
		}
		return m, nil
	case "r":
		m.status = "Refreshing sessions..."
		return m, loadSessionsCmd(srv.Conn)
	case "n":
		name := defaultSessionPrefix + "-" + time.Now().Format("150405")
		m.status = "Creating 2x2 grid " + name + "..."
		return m, createProfileSessionAttachCmd(srv.Conn, name, "grid-2x2")
	case "N":
		name := defaultSessionPrefix + "-" + time.Now().Format("150405")
		m.status = "Creating ops layout " + name + "..."
		return m, createProfileSessionAttachCmd(srv.Conn, name, "ops-2w")
	case "enter":
		sel, ok := m.selectedSessionInfo()
		if !ok {
			m.status = "No session selected"
			return m, nil
		}
		m.status = "Attaching to " + sel.Name + "..."
		return m, attachRemoteCmd(srv.Conn, "tmux attach-session -t "+shellQuote(sel.Name), "Detached from "+sel.Name)
	}
	return m, nil
}

func (m model) View() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Render("echoshell")
	status := lipgloss.NewStyle().Foreground(lipgloss.Color("111")).Render("status: " + m.status)

	switch m.screen {
	case screenServers:
		return lipgloss.JoinVertical(lipgloss.Left,
			title,
			m.viewServers(),
			status,
			"keys: j/k move  enter connect  a add server  d delete  q quit",
		)
	case screenAddServerName:
		return lipgloss.JoinVertical(lipgloss.Left,
			title,
			"Add server - name",
			"> "+m.input,
			status,
			"keys: enter continue  esc cancel",
		)
	case screenAddServerConn:
		return lipgloss.JoinVertical(lipgloss.Left,
			title,
			"Add server - connection",
			"name: "+m.pendingName,
			"> "+m.input,
			"example: root@192.168.1.10",
			status,
			"keys: enter save  esc cancel",
		)
	case screenSessions:
		return lipgloss.JoinVertical(lipgloss.Left,
			title,
			m.viewSessions(),
			status,
			"keys: j/k move  enter attach  n new 2x2  N new ops(2w)  r refresh  b back  q quit",
		)
	default:
		return title
	}
}

func (m model) viewServers() string {
	box := lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220")).Render("Servers")

	if len(m.servers) == 0 {
		return box.Render(title + "\n\n(no servers saved)")
	}

	selectedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62")).Padding(0, 1)
	normalStyle := lipgloss.NewStyle().Padding(0, 1)

	lines := []string{title, ""}
	for i, s := range m.servers {
		line := fmt.Sprintf("%s -> %s", s.Name, s.Conn)
		if i == m.selectedServer {
			lines = append(lines, selectedStyle.Render(line))
		} else {
			lines = append(lines, normalStyle.Render(line))
		}
	}
	return box.Render(strings.Join(lines, "\n"))
}

func (m model) viewSessions() string {
	box := lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)
	srv, _ := m.currentServer()
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220")).Render("Connected: " + srv.Name + " (" + srv.Conn + ")")

	if len(m.sessions) == 0 {
		return box.Render(title + "\n\n(no tmux sessions)\n\npress n to create one")
	}

	selectedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62")).Padding(0, 1)
	normalStyle := lipgloss.NewStyle().Padding(0, 1)

	lines := []string{title, ""}
	for i, s := range m.sessions {
		marker := " "
		if s.Attached {
			marker = "*"
		}
		line := fmt.Sprintf("%s %s", marker, s.Name)
		if s.Profile != "" {
			line += " [" + s.Profile + "]"
		}
		if i == m.selectedSession {
			lines = append(lines, selectedStyle.Render(line))
		} else {
			lines = append(lines, normalStyle.Render(line))
		}
	}
	return box.Render(strings.Join(lines, "\n"))
}

func (m model) selectedServerProfile() (serverProfile, bool) {
	if len(m.servers) == 0 || m.selectedServer < 0 || m.selectedServer >= len(m.servers) {
		return serverProfile{}, false
	}
	return m.servers[m.selectedServer], true
}

func (m model) currentServer() (serverProfile, bool) {
	if m.connected < 0 || m.connected >= len(m.servers) {
		return serverProfile{}, false
	}
	return m.servers[m.connected], true
}

func (m model) selectedSessionInfo() (sessionInfo, bool) {
	if len(m.sessions) == 0 || m.selectedSession < 0 || m.selectedSession >= len(m.sessions) {
		return sessionInfo{}, false
	}
	return m.sessions[m.selectedSession], true
}

func loadServersCmd() tea.Cmd {
	return func() tea.Msg {
		servers, err := loadServers()
		return serversLoadedMsg{servers: servers, err: err}
	}
}

func saveServersCmd(servers []serverProfile) tea.Cmd {
	copyServers := append([]serverProfile(nil), servers...)
	return func() tea.Msg {
		if err := saveServers(copyServers); err != nil {
			return actionDoneMsg{err: err}
		}
		return actionDoneMsg{status: "Saved servers"}
	}
}

func loadSessionsCmd(conn string) tea.Cmd {
	return func() tea.Msg {
		sessions, err := listRemoteSessions(conn)
		return sessionsLoadedMsg{sessions: sessions, err: err}
	}
}

func attachRemoteCmd(conn, remoteCommand, onReturn string) tea.Cmd {
	return tea.ExecProcess(exec.Command("ssh", "-t", conn, remoteCommand), func(err error) tea.Msg {
		if err != nil {
			return actionDoneMsg{err: err}
		}
		return actionDoneMsg{status: onReturn}
	})
}

func createProfileSessionAttachCmd(conn, name, profile string) tea.Cmd {
	remoteCommand := buildCreateProfileScript(name, profile)
	return tea.ExecProcess(exec.Command("ssh", "-t", conn, remoteCommand), func(err error) tea.Msg {
		if err != nil {
			return actionDoneMsg{err: err}
		}
		return actionDoneMsg{status: "Detached from " + name}
	})
}

func refreshTickCmd() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return refreshTickMsg(t) })
}

func listRemoteSessions(conn string) ([]sessionInfo, error) {
	out, err := runOut("ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=8", conn, "tmux list-sessions -F '#{session_name}|#{session_attached}|#{@echoshell_profile}'")
	if err != nil {
		s := err.Error()
		if strings.Contains(s, "no server running") || strings.Contains(s, "failed to connect to server") {
			return nil, nil
		}
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}

	lines := strings.Split(out, "\n")
	sessions := make([]sessionInfo, 0, len(lines))
	for _, line := range lines {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(parts) < 2 {
			continue
		}
		profile := ""
		if len(parts) == 3 {
			profile = strings.TrimSpace(parts[2])
		}
		sessions = append(sessions, sessionInfo{
			Name:     strings.TrimSpace(parts[0]),
			Attached: strings.TrimSpace(parts[1]) == "1",
			Profile:  profile,
		})
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Name < sessions[j].Name })
	return sessions, nil
}

func upsertServer(list []serverProfile, item serverProfile) []serverProfile {
	for i := range list {
		if list[i].Name == item.Name {
			list[i] = item
			return list
		}
	}
	return append(list, item)
}

func loadServers() ([]serverProfile, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var cfg serverConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	return cfg.Servers, nil
}

func saveServers(servers []serverProfile) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	cfg := serverConfig{Servers: servers}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func configPath() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "echoshell", "servers.json"), nil
}

func runOut(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
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

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n'\"`$&|;<>*?[]{}()!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func buildCreateProfileScript(name, profile string) string {
	qName := shellQuote(name)
	qProfile := shellQuote(profile)

	var body string
	switch profile {
	case "ops-2w":
		body = strings.Join([]string{
			"tmux new-session -d -s " + qName + " -n main",
			"tmux split-window -h -t " + qName + ":0.0",
			"tmux split-window -v -t " + qName + ":0.0",
			"tmux split-window -v -t " + qName + ":0.1",
			"tmux select-layout -t " + qName + ":0 tiled",
			"tmux new-window -t " + qName + " -n ops",
			"tmux select-window -t " + qName + ":0",
		}, "; ")
	default:
		body = strings.Join([]string{
			"tmux new-session -d -s " + qName + " -n main",
			"tmux split-window -h -t " + qName + ":0.0",
			"tmux split-window -v -t " + qName + ":0.0",
			"tmux split-window -v -t " + qName + ":0.1",
			"tmux select-layout -t " + qName + ":0 tiled",
		}, "; ")
	}

	metadata := strings.Join([]string{
		"tmux set-option -t " + qName + " @echoshell_profile " + qProfile,
		"tmux set-option -t " + qName + " @echoshell_layout_version 1",
		"tmux set-option -t " + qName + " mouse on",
	}, "; ")

	createIfMissing := "tmux has-session -t " + qName + " 2>/dev/null || (" + body + "; " + metadata + ")"
	attach := "tmux attach-session -t " + qName

	return "sh -lc " + shellQuote(createIfMissing+"; "+attach)
}
