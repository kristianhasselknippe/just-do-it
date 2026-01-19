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
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
)

type state int

const (
	viewList state = iota
	viewInput
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
}

// recipeItem implements list.Item
type recipeItem struct {
	name, desc string
}

func (i recipeItem) Title() string       { return i.name }
func (i recipeItem) Description() string { return i.desc }
func (i recipeItem) FilterValue() string { return i.name }

type model struct {
	list           list.Model
	viewport       viewport.Model
	input          textinput.Model
	state          state
	recipes        map[string]Recipe
	selectedRecipe *Recipe
	ready          bool
	err            error
	terminalWidth  int
	terminalHeight int
	finalCmd       []string // Command to exec on exit
}

func main() {
	m := model{
		recipes: make(map[string]Recipe),
		state:   viewList,
		input:   textinput.New(),
	}

	m.input.Placeholder = "Enter arguments..."
	m.input.CharLimit = 100
	m.input.Width = 50

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

	// Setup list
	delegate := list.NewDefaultDelegate()
	m.list = list.New(items, delegate, 0, 0)
	m.list.Title = "Just Tasks"
	m.list.SetShowHelp(false)

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
				// Select task (works for both browsing and filtering)
				if i, ok := m.list.SelectedItem().(recipeItem); ok {
					recipe := m.recipes[i.name]
					m.selectedRecipe = &recipe

					// Check params
					if len(recipe.Parameters) > 0 {
						m.state = viewInput
						m.input.Focus()
						return m, textinput.Blink
					} else {
						// Execute immediately
						m.finalCmd = []string{"just", i.name}
						return m, tea.Quit
					}
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
				// We want to support navigation keys (j/k) if not typing?
				// User said: "Writing anything should just start filtering".
				// This implies fzf-style behavior where typing (even j/k) filters.
				// Navigation must be done via arrows.

				// Send a synthetic '/' key to start filtering
				var cmd tea.Cmd
				m.list, cmd = m.list.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
				cmds = append(cmds, cmd)
				// The original key will be processed by the list update below
			}
		} else if m.state == viewInput {
			// Input view specific keys
			switch msg.String() {
			case "esc":
				m.state = viewList
				m.input.Reset()
				return m, nil
			case "enter":
				// Execute with args
				args := strings.Fields(m.input.Value())
				cmdSlice := append([]string{"just", m.selectedRecipe.Name}, args...)
				m.finalCmd = cmdSlice
				return m, tea.Quit
			}
		}

	case tea.WindowSizeMsg:
		m.terminalWidth = msg.Width
		m.terminalHeight = msg.Height

		// Layout logic
		listWidth := int(float64(msg.Width) * 0.35)
		viewportWidth := msg.Width - listWidth - 4 // minus padding/borders

		headerHeight := lipgloss.Height(m.list.Title)
		footerHeight := 2

		m.list.SetSize(listWidth, msg.Height-headerHeight-footerHeight)

		if !m.ready {
			m.viewport = viewport.New(viewportWidth, msg.Height-2)
			m.viewport.HighPerformanceRendering = false
			m.ready = true
		} else {
			m.viewport.Width = viewportWidth
			m.viewport.Height = msg.Height - 2
		}

		if m.list.SelectedItem() != nil {
			cmds = append(cmds, m.updateViewportContent(m.list.SelectedItem().(recipeItem).name))
		}

	case recipeContentMsg:
		m.viewport.SetContent(string(msg))

	case error:
		m.err = msg
		return m, nil
	}

	// Only update active component
	if m.state == viewList {
		prevItem := m.list.SelectedItem()
		m.list, cmd = m.list.Update(msg)
		cmds = append(cmds, cmd)

		currItem := m.list.SelectedItem()
		if currItem != nil && (prevItem == nil || prevItem.(recipeItem).name != currItem.(recipeItem).name) {
			cmds = append(cmds, m.updateViewportContent(currItem.(recipeItem).name))
		}

		if _, ok := msg.(tea.WindowSizeMsg); ok && currItem != nil {
			cmds = append(cmds, m.updateViewportContent(currItem.(recipeItem).name))
		}

		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
	} else {
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
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

	if m.state == viewInput {
		return m.inputView()
	}

	listStyle := lipgloss.NewStyle().MarginRight(2)
	viewportStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("62")).
		Padding(0, 1)

	return lipgloss.JoinHorizontal(
		lipgloss.Top,
		listStyle.Render(m.list.View()),
		viewportStyle.Width(m.viewport.Width).Height(m.viewport.Height).Render(m.viewport.View()),
	)
}

func (m model) inputView() string {
	var b strings.Builder

	// Title
	b.WriteString(titleStyle.Render("Run Task: " + m.selectedRecipe.Name))
	b.WriteString("\n\n")

	// Parameters help
	if len(m.selectedRecipe.Parameters) > 0 {
		b.WriteString("Parameters:\n")
		for _, p := range m.selectedRecipe.Parameters {
			line := fmt.Sprintf("  %s", p.Name)
			if p.Default != nil {
				line += fmt.Sprintf(" (default: %s)", *p.Default)
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString(m.input.View())
	b.WriteString("\n\n(Enter to run, Esc to cancel)")

	// Center logic could be here, but simple render is fine
	return lipgloss.Place(
		m.terminalWidth,
		m.terminalHeight,
		lipgloss.Center,
		lipgloss.Center,
		b.String(),
	)
}
