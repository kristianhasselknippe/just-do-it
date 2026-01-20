package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"
)

var (
	appStyle = lipgloss.NewStyle().Padding(1, 2)

	titleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFFDF5")).
			Background(lipgloss.Color("#25A065")).
			Padding(0, 1)

	statusMessageStyle = lipgloss.NewStyle().
				Foreground(lipgloss.AdaptiveColor{Light: "#04B575", Dark: "#04B575"}).
				Render

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))
)

type state int

const (
	viewList state = iota
	viewInput
	viewGenerating
	viewApiKeyInput
	viewProviderSelect
	viewModelInput
	viewModelSelect
)

// Data structures for parsing 'just --dump --dump-format json'
type JustDump struct {
	Recipes map[string]Recipe `json:"recipes"`
}

type Recipe struct {
	Name         string       `json:"name"`
	Doc          *string      `json:"doc"` // Use pointer for nullable
	Dependencies []Dependency `json:"dependencies"`
	Parameters   []Parameter  `json:"parameters"`
	// We ignore Body for now as it's complex AST
}

type Dependency struct {
	Recipe string `json:"recipe"`
}

type Parameter struct {
	Name    string  `json:"name"`
	Default *string `json:"default"`
	Kind    string  `json:"kind"`
}

// recipeItem implements list.Item
type recipeItem struct {
	name, desc string
}

func (i recipeItem) Title() string       { return i.name }
func (i recipeItem) Description() string { return i.desc }
func (i recipeItem) FilterValue() string { return i.name }

type aiItem struct {
	prompt *string
}

func (a aiItem) Title() string {
	if a.prompt == nil || *a.prompt == "" {
		return "✨ Generate command with AI"
	}
	return fmt.Sprintf("✨ Generate command for: %s", *a.prompt)
}
func (a aiItem) Description() string { return "Use AI to generate a bash command" }
func (a aiItem) FilterValue() string { return "" }

type model struct {
	list           list.Model
	viewport       viewport.Model
	inputs         []textinput.Model
	modelList      list.Model // New list for models
	spinner        spinner.Model
	focusIndex     int
	providerIndex  int // Track selected provider
	state          state
	recipes        map[string]Recipe
	selectedRecipe *Recipe
	ready          bool
	err            error
	terminalWidth  int
	terminalHeight int
	finalCmd       []string
	aiPrompt       *string // Shared pointer for AI item title
	streamContent  string
	streamChan     chan streamResult
}

type streamResult struct {
	chunk string
	err   error
	done  bool
}

// modelItem implements list.Item for model selection
type modelItem string

func (m modelItem) Title() string       { return string(m) }
func (m modelItem) Description() string { return "" }
func (m modelItem) FilterValue() string { return string(m) }

// logDebug writes to a debug file
func logDebug(format string, args ...interface{}) {
	f, err := os.OpenFile("debug.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s: %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

func main() {
	logDebug("Application started")
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	m := model{
		recipes:  make(map[string]Recipe),
		state:    viewList,
		spinner:  s,
		aiPrompt: new(string),
	}

	// Fetch recipes
	dump, err := getJustDump()
	if err != nil {
		fmt.Printf("Error fetching recipes: %v\n", err)
		os.Exit(1)
	}
	m.recipes = dump.Recipes

	// Prepare list items
	items := []list.Item{}
	for _, r := range m.recipes {
		desc := ""
		if r.Doc != nil {
			desc = *r.Doc
		}
		items = append(items, recipeItem{name: r.Name, desc: desc})
	}

	// Sort items by name
	sort.Slice(items, func(i, j int) bool {
		return items[i].(recipeItem).name < items[j].(recipeItem).name
	})

	// Append AI item
	items = append(items, aiItem{prompt: m.aiPrompt})

	// Setup list
	delegate := list.NewDefaultDelegate()
	m.list = list.New(items, delegate, 0, 0)
	m.list.Title = "Just Tasks"
	m.list.SetShowHelp(false)

	// Custom filter to always include AI item
	m.list.Filter = func(term string, targets []string) []list.Rank {
		// If targets is empty, return nil
		if len(targets) == 0 {
			return nil
		}

		// Real targets are all except the last one (AI item)
		realTargets := targets[:len(targets)-1]
		matches := fuzzy.Find(term, realTargets)

		ranks := make([]list.Rank, len(matches))
		for i, match := range matches {
			ranks[i] = list.Rank{
				Index:          match.Index,
				MatchedIndexes: match.MatchedIndexes,
			}
		}

		// Always append AI item (last item)
		ranks = append(ranks, list.Rank{
			Index: len(targets) - 1,
		})

		return ranks
	}

	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	finalModel, err := p.Run()
	if err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}

	// Handle execution after TUI exit
	if m, ok := finalModel.(model); ok && len(m.finalCmd) > 0 {
		// Use syscall.Exec to replace the process
		binary, lookErr := exec.LookPath(m.finalCmd[0])
		if lookErr != nil {
			fmt.Fprintf(os.Stderr, "Error finding command %s: %v\n", m.finalCmd[0], lookErr)
			os.Exit(1)
		}

		// syscall.Exec requires the full path as the first argument,
		// and the slice of arguments (including the command name) as the second.
		// Environment variables are passed as the third argument.
		execErr := syscall.Exec(binary, m.finalCmd, os.Environ())
		if execErr != nil {
			fmt.Fprintf(os.Stderr, "Error executing command: %v\n", execErr)
			os.Exit(1)
		}
	}
}

func getJustDump() (*JustDump, error) {
	cmd := exec.Command("just", "--dump", "--dump-format", "json")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var dump JustDump
	if err := json.Unmarshal(output, &dump); err != nil {
		return nil, err
	}
	return &dump, nil
}

// Msg to update viewport content
type recipeContentMsg string

func (m model) Init() tea.Cmd {
	return tea.EnterAltScreen
}

// Msg to paste text into input
type pasteMsg string

// Msg for AI completion
type aiCompletionMsg string

// Msg when models are fetched
type modelsFetchedMsg []string

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmds []tea.Cmd
	)

	// Reset error on key press
	if _, ok := msg.(tea.KeyMsg); ok {
		m.err = nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		}

		if m.state == viewList {
			switch msg.String() {
			case "ctrl+p":
				m.state = viewProviderSelect
				m.providerIndex = 0
				return m, nil
			case "enter":
				// Check if AI item selected
				if item, ok := m.list.SelectedItem().(aiItem); ok {
					m.state = viewGenerating
					prompt := *item.prompt
					m.streamContent = ""
					ch := make(chan streamResult, 100)
					m.streamChan = ch

					go func() {
						defer close(ch)
						ctx := context.Background()
						_, err := GenerateCommand(ctx, prompt, func(s string) {
							ch <- streamResult{chunk: s}
						})
						if err != nil {
							ch <- streamResult{err: err}
						}
						ch <- streamResult{done: true}
					}()

					return m, tea.Batch(
						m.spinner.Tick,
						waitForStream(ch),
					)
				}

				// Select task
				if i, ok := m.list.SelectedItem().(recipeItem); ok {
					recipe := m.recipes[i.name]
					m.selectedRecipe = &recipe

					if len(recipe.Parameters) > 0 {
						m.state = viewInput
						m.inputs = make([]textinput.Model, len(recipe.Parameters))
						for i, p := range recipe.Parameters {
							t := textinput.New()
							t.Prompt = fmt.Sprintf("%s: ", p.Name)
							t.Width = 50
							if p.Default != nil {
								t.Placeholder = fmt.Sprintf("%s (default)", *p.Default)
							}
							if i == 0 {
								t.Focus()
							}
							m.inputs[i] = t
						}
						m.focusIndex = 0
						return m, textinput.Blink
					} else {
						m.finalCmd = []string{"just", i.name}
						return m, tea.Quit
					}
				}
			case "q":
				if !m.list.SettingFilter() {
					return m, tea.Quit
				}
			case "esc":
				if m.list.SettingFilter() {
					m.list.ResetFilter()
					return m, nil
				}
				return m, tea.Quit
			}

			if !m.list.SettingFilter() && msg.Type == tea.KeyRunes {
				var cmd tea.Cmd
				m.list, cmd = m.list.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
				cmds = append(cmds, cmd)
			}

		} else if m.state == viewInput || m.state == viewApiKeyInput || m.state == viewProviderSelect || m.state == viewModelInput {
			switch msg.String() {
			case "esc":
				m.state = viewList
				m.inputs = nil
				return m, nil

			case "tab", "shift+tab", "up", "down":
				if m.state == viewApiKeyInput || m.state == viewModelInput {
					return m, nil
				}
				if m.state == viewProviderSelect {
					if msg.String() == "up" {
						if m.providerIndex > 0 {
							m.providerIndex--
						}
					} else if msg.String() == "down" {
						if m.providerIndex < 1 {
							m.providerIndex++
						}
					}
					return m, nil
				}

				s := msg.String()
				if s == "up" || s == "shift+tab" {
					m.focusIndex--
				} else {
					m.focusIndex++
				}

				if m.focusIndex > len(m.inputs)-1 {
					m.focusIndex = 0
				} else if m.focusIndex < 0 {
					m.focusIndex = len(m.inputs) - 1
				}

				cmds := make([]tea.Cmd, len(m.inputs))
				for i := 0; i <= len(m.inputs)-1; i++ {
					if i == m.focusIndex {
						cmds[i] = m.inputs[i].Focus()
						continue
					}
					m.inputs[i].Blur()
				}
				return m, tea.Batch(cmds...)

			case "enter":
				if m.state == viewProviderSelect {
					// Check for existing key first?
					// Flow: Select Provider -> Check Config/Env -> If missing ask Key -> Fetch Models -> Select Model

					cfg, _ := LoadConfig()
					if cfg == nil {
						cfg = &Config{}
					}

					var key string
					if m.providerIndex == 0 {
						key = cfg.GoogleAPIKey
						if key == "" {
							key = os.Getenv("GOOGLE_API_KEY")
						}
					} else {
						key = cfg.OpenAIAPIKey
						if key == "" {
							key = os.Getenv("OPENAI_API_KEY")
						}
					}

					if key == "" {
						m.state = viewApiKeyInput
						t := textinput.New()
						t.Placeholder = "Key..."
						t.EchoMode = textinput.EchoPassword
						t.Width = 50
						t.Focus()
						m.inputs = []textinput.Model{t}
						m.focusIndex = 0
						return m, nil
					}

					// Have key, fetch models
					m.state = viewGenerating // Reuse loading state
					provider := "google"
					if m.providerIndex == 1 {
						provider = "openai"
					}

					return m, tea.Batch(
						m.spinner.Tick,
						func() tea.Msg {
							models, err := ListModels(provider, key)
							if err != nil {
								// Fallback to manual input if list fails
								return fmt.Errorf("list_models_failed")
							}
							return modelsFetchedMsg(models)
						},
					)
				}

				if m.state == viewApiKeyInput {
					key := m.inputs[0].Value()
					if key != "" {
						cfg, _ := LoadConfig()
						if cfg == nil {
							cfg = &Config{}
						}
						if m.providerIndex == 0 {
							cfg.GoogleAPIKey = key
						} else {
							cfg.OpenAIAPIKey = key
						}
						if err := SaveConfig(cfg); err != nil {
							m.err = fmt.Errorf("failed to save config: %v", err)
							return m, nil
						}

						// Now fetch models
						m.state = viewGenerating
						provider := "google"
						if m.providerIndex == 1 {
							provider = "openai"
						}

						return m, tea.Batch(
							m.spinner.Tick,
							func() tea.Msg {
								models, err := ListModels(provider, key)
								if err != nil {
									return fmt.Errorf("list_models_failed")
								}
								return modelsFetchedMsg(models)
							},
						)
					}
					return m, nil
				}

				if m.state == viewModelInput {
					// Manual entry fallback
					model := m.inputs[0].Value()
					cfg, _ := LoadConfig()
					if cfg == nil {
						cfg = &Config{}
					}

					if model != "" {
						if m.providerIndex == 0 {
							cfg.GoogleModel = model
						} else {
							cfg.OpenAIModel = model
						}
						SaveConfig(cfg)
					}
					m.state = viewList
					m.inputs = nil
					return m, nil
				}

				if m.focusIndex < len(m.inputs)-1 {

					m.inputs[m.focusIndex].Blur()
					m.focusIndex++
					m.inputs[m.focusIndex].Focus()
					return m, textinput.Blink
				}

				args := []string{}
				for i, input := range m.inputs {
					val := input.Value()
					if val == "" && m.selectedRecipe.Parameters[i].Default != nil {
						val = *m.selectedRecipe.Parameters[i].Default
					}
					if m.selectedRecipe.Parameters[i].Kind == "plus" || m.selectedRecipe.Parameters[i].Kind == "star" {
						args = append(args, strings.Fields(val)...)
					} else {
						args = append(args, val)
					}
				}

				if m.selectedRecipe.Name == "AI Command" {
					m.finalCmd = []string{"sh", "-c", args[0]}
				} else {
					cmdSlice := append([]string{"just", m.selectedRecipe.Name}, args...)
					m.finalCmd = cmdSlice
				}
				return m, tea.Quit

			case "ctrl+f":
				c := exec.Command("fzf")
				var out bytes.Buffer
				c.Stdout = &out
				c.Stdin = os.Stdin
				c.Stderr = os.Stderr
				return m, tea.ExecProcess(c, func(err error) tea.Msg {
					if err != nil {
						return nil
					}
					return pasteMsg(strings.TrimSpace(out.String()))
				})
			}
		}

	case pasteMsg:
		if (m.state == viewInput || m.state == viewApiKeyInput || m.state == viewModelInput) && len(msg) > 0 {
			input := m.inputs[m.focusIndex]
			val := input.Value()
			cursor := input.Position()
			newVal := ""
			if cursor >= len(val) {
				newVal = val + string(msg)
			} else {
				newVal = val[:cursor] + string(msg) + val[cursor:]
			}
			input.SetValue(newVal)
			input.SetCursor(cursor + len(msg))
			m.inputs[m.focusIndex] = input
		}

	case streamResult:
		logDebug("Update received streamResult: chunk=%q done=%v err=%v", msg.chunk, msg.done, msg.err)
		if msg.err != nil {
			if msg.err.Error() == "MISSING_API_KEY" {
				m.state = viewProviderSelect
				m.err = nil
				m.providerIndex = 0
				return m, nil
			}
			m.err = fmt.Errorf("AI Error: %v", msg.err)
			m.state = viewList
			return m, nil
		}
		if msg.done {
			return m, func() tea.Msg { return aiCompletionMsg(m.streamContent) }
		}
		m.streamContent += msg.chunk
		return m, waitForStream(m.streamChan)

	case aiCompletionMsg:
		m.state = viewInput
		m.selectedRecipe = &Recipe{
			Name:       "AI Command",
			Parameters: []Parameter{{Name: "command", Default: nil}},
		}
		t := textinput.New()
		t.Prompt = "Run: "
		t.Width = m.terminalWidth - 10
		t.SetValue(string(msg))
		t.Focus()
		m.inputs = []textinput.Model{t}
		m.focusIndex = 0
		return m, textinput.Blink

	case spinner.TickMsg:
		if m.state == viewGenerating {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}

	case tea.WindowSizeMsg:
		m.terminalWidth = msg.Width
		m.terminalHeight = msg.Height

		listWidth := int(float64(msg.Width) * 0.35)
		viewportWidth := msg.Width - listWidth - 8

		headerHeight := lipgloss.Height(m.list.Title)
		listFooterHeight := 2
		globalFooterHeight := 1

		m.list.SetSize(listWidth, msg.Height-headerHeight-listFooterHeight-globalFooterHeight)

		if !m.ready {
			m.viewport = viewport.New(viewportWidth, msg.Height-2-globalFooterHeight)
			m.viewport.HighPerformanceRendering = false
			m.ready = true
		} else {
			m.viewport.Width = viewportWidth
			m.viewport.Height = msg.Height - 2 - globalFooterHeight
		}

		if m.list.SelectedItem() != nil {
			if i, ok := m.list.SelectedItem().(recipeItem); ok {
				cmds = append(cmds, m.updateViewportContent(i.name))
			}
		}

	case recipeContentMsg:
		content := string(msg)
		if m.viewport.Width > 0 {
			content = lipgloss.NewStyle().Width(m.viewport.Width).Render(content)
		}
		m.viewport.SetContent(content)

	case modelsFetchedMsg:
		m.state = viewModelSelect
		items := []list.Item{}
		for _, name := range msg {
			items = append(items, modelItem(name))
		}

		// Setup model list
		delegate := list.NewDefaultDelegate()
		m.modelList = list.New(items, delegate, 0, 0)
		m.modelList.Title = "Select Model"
		m.modelList.SetShowHelp(false)

		// Use fuzzy filter
		m.modelList.Filter = func(term string, targets []string) []list.Rank {
			if len(targets) == 0 {
				return nil
			}
			matches := fuzzy.Find(term, targets)
			ranks := make([]list.Rank, len(matches))
			for i, match := range matches {
				ranks[i] = list.Rank{
					Index:          match.Index,
					MatchedIndexes: match.MatchedIndexes,
				}
			}
			return ranks
		}

		// Set size

		headerHeight := lipgloss.Height(m.modelList.Title)
		m.modelList.SetSize(m.terminalWidth, m.terminalHeight-headerHeight-2)
		return m, nil

	case error:
		if msg.Error() == "MISSING_API_KEY" {
			m.state = viewProviderSelect
			m.err = nil
			m.providerIndex = 0
			return m, nil
		}
		if msg.Error() == "list_models_failed" {
			// Fallback to manual input
			m.state = viewModelInput
			t := textinput.New()
			t.Placeholder = "Model ID (e.g. gemini-2.0-flash)"
			t.Width = 50
			t.Focus()
			m.inputs = []textinput.Model{t}
			m.focusIndex = 0
			m.err = nil // Clear error
			return m, nil
		}
		m.err = msg
		return m, nil
	}

	if m.state == viewList {
		prevItem := m.list.SelectedItem()
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		cmds = append(cmds, cmd)

		*m.aiPrompt = m.list.FilterValue()

		currItem := m.list.SelectedItem()
		if currItem != nil {
			if i, ok := currItem.(recipeItem); ok {
				if prevItem == nil || prevItem.FilterValue() != i.name {
					cmds = append(cmds, m.updateViewportContent(i.name))
				}
				if _, ok := msg.(tea.WindowSizeMsg); ok {
					cmds = append(cmds, m.updateViewportContent(i.name))
				}
			} else if _, ok := currItem.(aiItem); ok {
				m.viewport.SetContent(lipgloss.NewStyle().Width(m.viewport.Width).Render("Select to generate a command using AI based on your search text."))
			}
		}

		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		cmds = append(cmds, vpCmd)
	} else if m.state == viewGenerating {
		// wait
	} else if m.state == viewModelSelect {
		var cmd tea.Cmd

		// Handle typing to filter instantly
		if !m.modelList.SettingFilter() {
			if keyMsg, ok := msg.(tea.KeyMsg); ok && keyMsg.Type == tea.KeyRunes {
				m.modelList, cmd = m.modelList.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
				cmds = append(cmds, cmd)
			}
		}

		m.modelList, cmd = m.modelList.Update(msg)
		cmds = append(cmds, cmd)

		// Check for enter key specifically since list consumes it
		if keyMsg, ok := msg.(tea.KeyMsg); ok && keyMsg.String() == "enter" {
			if i, ok := m.modelList.SelectedItem().(modelItem); ok {
				cfg, _ := LoadConfig()
				if cfg == nil {
					cfg = &Config{}
				}

				if m.providerIndex == 0 {
					cfg.GoogleModel = string(i)
				} else {
					cfg.OpenAIModel = string(i)
				}
				SaveConfig(cfg)
				m.state = viewList
				return m, nil
			}
		}
		// Check for esc
		if keyMsg, ok := msg.(tea.KeyMsg); ok && keyMsg.String() == "esc" {
			m.state = viewList
			return m, nil
		}

	} else {
		for i := range m.inputs {
			var cmd tea.Cmd
			m.inputs[i], cmd = m.inputs[i].Update(msg)
			cmds = append(cmds, cmd)
		}
	}

	return m, tea.Batch(cmds...)
}

func (m model) updateViewportContent(recipeName string) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("just", "--color", "always", "--show", recipeName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return recipeContentMsg(fmt.Sprintf("Error fetching details: %v", err))
		}
		return recipeContentMsg(string(output))
	}
}

func (m model) View() string {
	if m.err != nil {
		return fmt.Sprintf("\nError: %v\nPress any key to dismiss.", m.err)
	}
	if !m.ready {
		return "\n  Initializing..."
	}

	var content string
	if m.state == viewInput || m.state == viewApiKeyInput || m.state == viewProviderSelect || m.state == viewModelInput {
		content = m.inputView()
	} else if m.state == viewModelSelect {
		listStyle := lipgloss.NewStyle().Margin(1, 2)
		content = listStyle.Render(m.modelList.View())
	} else if m.state == viewGenerating {
		header := fmt.Sprintf("\n\n   %s Generating command...", m.spinner.View())

		var output string
		if m.streamContent != "" {
			output = lipgloss.NewStyle().
				Foreground(lipgloss.Color("205")).
				Padding(1, 2).
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("62")).
				Render(m.streamContent)
		}

		content = lipgloss.JoinVertical(lipgloss.Center, header, output)
		content = lipgloss.Place(m.terminalWidth, m.terminalHeight-1, lipgloss.Center, lipgloss.Center, content)
	} else {
		listStyle := lipgloss.NewStyle().MarginRight(2)
		viewportStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62")).
			Padding(0, 1)

		content = lipgloss.JoinHorizontal(
			lipgloss.Top,
			listStyle.Render(m.list.View()),
			viewportStyle.Width(m.viewport.Width).Height(m.viewport.Height).Render(m.viewport.View()),
		)
	}

	return lipgloss.JoinVertical(lipgloss.Left, content, m.footerView())
}

func waitForStream(ch <-chan streamResult) tea.Cmd {
	return func() tea.Msg {
		res, ok := <-ch
		if !ok {
			return nil
		}
		return res
	}
}

func (m model) footerView() string {
	var keys []string
	if m.state == viewList {
		keys = []string{"↑/↓/j/k: navigate", "enter: select", "type: search", "ctrl+p: ai settings", "q: quit"}
	} else if m.state == viewInput {
		keys = []string{"tab/shift+tab: nav fields", "ctrl+f: find file", "enter: run", "esc: cancel"}
	} else if m.state == viewApiKeyInput {
		keys = []string{"enter: next", "esc: cancel"}
	} else if m.state == viewProviderSelect {
		keys = []string{"↑/↓: select provider", "enter: next", "esc: cancel"}
	} else if m.state == viewModelInput {
		keys = []string{"enter: save", "esc: cancel"}
	} else if m.state == viewModelSelect {
		keys = []string{"↑/↓: navigate", "enter: select", "type: filter", "esc: cancel"}
	}
	// Join with some spacing and styling. Ensure it spans full width or looks good.
	return helpStyle.Render(strings.Join(keys, " • "))
}

func (m model) inputView() string {
	var b strings.Builder

	if m.state == viewProviderSelect {
		b.WriteString(titleStyle.Render("Select AI Provider"))
		b.WriteString("\n\n")

		providers := []string{"Google Gemini", "OpenAI"}
		for i, p := range providers {
			cursor := " "
			if m.providerIndex == i {
				cursor = ">"
			}
			// Simple highlighting
			if m.providerIndex == i {
				b.WriteString(fmt.Sprintf("%s %s\n", cursor, lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Render(p)))
			} else {
				b.WriteString(fmt.Sprintf("%s %s\n", cursor, p))
			}
		}

		return lipgloss.Place(
			m.terminalWidth,
			m.terminalHeight-1,
			lipgloss.Center,
			lipgloss.Center,
			b.String(),
		)
	}

	if m.state == viewApiKeyInput {
		b.WriteString(titleStyle.Render("Enter API Key"))
		b.WriteString("\n\n")
		b.WriteString("Please enter your Google (Gemini) or OpenAI API Key.\nIt will be saved to your config file.\n\n")
		b.WriteString(m.inputs[0].View())

		return lipgloss.Place(
			m.terminalWidth,
			m.terminalHeight-1,
			lipgloss.Center,
			lipgloss.Center,
			b.String(),
		)
	}

	if m.state == viewModelInput {
		b.WriteString(titleStyle.Render("Enter Model Name"))
		b.WriteString("\n\n")

		defaultModel := "gemini-2.0-flash"
		if m.providerIndex == 1 {
			defaultModel = "gpt-4o"
		}

		b.WriteString(fmt.Sprintf("Enter the model ID to use (default: %s).\nLeave empty to use default.\n\n", defaultModel))
		b.WriteString(m.inputs[0].View())

		return lipgloss.Place(
			m.terminalWidth,
			m.terminalHeight-1,
			lipgloss.Center,
			lipgloss.Center,
			b.String(),
		)
	}

	// Title
	b.WriteString(titleStyle.Render("Run Task: " + m.selectedRecipe.Name))
	b.WriteString("\n\n")

	// Render each input
	for i, input := range m.inputs {
		// Highlight the focused input prompt maybe?
		// textinput handles its own focus styling if Focus() is called.
		b.WriteString(input.View())
		b.WriteString("\n")
		// Add some spacing between inputs if needed
		if i < len(m.inputs)-1 {
			b.WriteString("\n")
		}
	}

	// Instructions moved to footer

	// Center logic could be here, but simple render is fine
	return lipgloss.Place(
		m.terminalWidth,
		m.terminalHeight-1, // Subtract footer height
		lipgloss.Center,
		lipgloss.Center,
		b.String(),
	)
}
