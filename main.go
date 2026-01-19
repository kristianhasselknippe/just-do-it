package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"

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
}

func main() {
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
		// If term is empty, bubbles/list handles it (usually)
		// But if called, return nil to match standard behavior?
		// Actually standard Filter returns matches.

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
	// Load the first item if it exists
	if len(m.recipes) > 0 {
		if len(m.list.Items()) > 0 {
			if i, ok := m.list.Items()[0].(recipeItem); ok {
				return m.updateViewportContent(i.name)
			}
		}
	}
	return nil
}

// Msg to paste text into input
type pasteMsg string

// Msg for AI completion
type aiCompletionMsg string

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
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
			// Handle special keys for the list view
			switch msg.String() {
			case "enter":
				// Check if AI item selected
				if item, ok := m.list.SelectedItem().(aiItem); ok {
					m.state = viewGenerating
					prompt := *item.prompt
					return m, tea.Batch(
						m.spinner.Tick,
						func() tea.Msg {
							cmdStr, err := GenerateCommand(prompt)
							if err != nil {
								if err.Error() == "MISSING_API_KEY" {
									return err
								}
								return fmt.Errorf("AI Error: %v", err)
							}
							return aiCompletionMsg(cmdStr)
						},
					)
				}

				// Select task (works for both browsing and filtering)
				if i, ok := m.list.SelectedItem().(recipeItem); ok {
					recipe := m.recipes[i.name]
					m.selectedRecipe = &recipe

					// Check params
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
						// Execute immediately
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

			// Auto-activate filter on typing
			if !m.list.SettingFilter() && msg.Type == tea.KeyRunes {
				// Send a synthetic '/' key to start filtering
				var cmd tea.Cmd
				m.list, cmd = m.list.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
				cmds = append(cmds, cmd)
			}
			// Update all inputs
			for i := range m.inputs {
				m.inputs[i], cmd = m.inputs[i].Update(msg)
				cmds = append(cmds, cmd)
			}
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
	if m.state == viewInput || m.state == viewApiKeyInput || m.state == viewProviderSelect {
		content = m.inputView()
	} else if m.state == viewGenerating {
		content = fmt.Sprintf("\n\n   %s Generating command...", m.spinner.View())
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

func (m model) footerView() string {
	var keys []string
	if m.state == viewList {
		keys = []string{"↑/↓/j/k: navigate", "enter: select", "type: search", "q: quit"}
	} else if m.state == viewInput {
		keys = []string{"tab/shift+tab: nav fields", "ctrl+f: find file", "enter: run", "esc: cancel"}
	} else if m.state == viewApiKeyInput {
		keys = []string{"enter: save key", "esc: cancel"}
	} else if m.state == viewProviderSelect {
		keys = []string{"↑/↓: select provider", "enter: next", "esc: cancel"}
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
