package main

import (
	"bytes"
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

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))
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
	Kind    string  `json:"kind"`
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
	inputs         []textinput.Model // Changed from single input to slice
	focusIndex     int               // Track focused input
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

// Msg to paste text into input
type pasteMsg string

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
			case "q":
				if !m.list.SettingFilter() {
					return m, tea.Quit
				}
			case "enter":
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
				m.inputs = nil
				return m, nil
			case "tab", "shift+tab", "up", "down":
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
						// Set focused state
						cmds[i] = m.inputs[i].Focus()
						continue
					}
					// Remove focused state
					m.inputs[i].Blur()
				}
				return m, tea.Batch(cmds...)

			case "enter":
				// If not on last input, move next
				if m.focusIndex < len(m.inputs)-1 {
					m.inputs[m.focusIndex].Blur()
					m.focusIndex++
					m.inputs[m.focusIndex].Focus()
					return m, textinput.Blink
				}

				// Execute with args
				args := []string{}
				for i, input := range m.inputs {
					val := input.Value()
					if val == "" && m.selectedRecipe.Parameters[i].Default != nil {
						val = *m.selectedRecipe.Parameters[i].Default
					}

					// Handle variadic args if necessary.
					// For now, if user typed spaces in a singular arg, we preserve it as one arg?
					// Just usually takes shell-split args.
					// But usually TUI inputs map 1-to-1 with params.
					// If the param kind is 'plus' or 'star' (variadic), we might want to Split it.
					// 'diff file1 file2' are singular, so we append as is.

					if m.selectedRecipe.Parameters[i].Kind == "plus" || m.selectedRecipe.Parameters[i].Kind == "star" {
						// Split variadic
						args = append(args, strings.Fields(val)...)
					} else {
						args = append(args, val)
					}
				}

				cmdSlice := append([]string{"just", m.selectedRecipe.Name}, args...)
				m.finalCmd = cmdSlice
				return m, tea.Quit
			case "ctrl+f":
				// Trigger fzf
				c := exec.Command("fzf")
				var out bytes.Buffer
				c.Stdout = &out
				c.Stdin = os.Stdin
				c.Stderr = os.Stderr

				return m, tea.ExecProcess(c, func(err error) tea.Msg {
					if err != nil {
						// fzf cancelled or error
						return nil
					}
					return pasteMsg(strings.TrimSpace(out.String()))
				})
			}
		}

	case pasteMsg:
		if m.state == viewInput && len(msg) > 0 {
			// Insert into currently focused input
			input := m.inputs[m.focusIndex]

			// Insert the text at the cursor
			val := input.Value()
			cursor := input.Position()

			// Simple insertion
			newVal := ""
			if cursor >= len(val) {
				newVal = val + string(msg)
			} else {
				newVal = val[:cursor] + string(msg) + val[cursor:]
			}
			input.SetValue(newVal)
			input.SetCursor(cursor + len(msg))

			// Update the model slice
			m.inputs[m.focusIndex] = input
		}

	case tea.WindowSizeMsg:
		m.terminalWidth = msg.Width
		m.terminalHeight = msg.Height

		// Layout logic
		listWidth := int(float64(msg.Width) * 0.35)

		// Calculation of available width for viewport:
		// Total Width = msg.Width
		// List takes: listWidth + 2 (MarginRight)
		// Viewport Style takes: 2 (Border) + 2 (Padding) = 4
		// Remaining for content: msg.Width - listWidth - 2 - 4 = msg.Width - listWidth - 6
		// We add a little extra safety buffer (-2) to prevent edge-case wrapping
		viewportWidth := msg.Width - listWidth - 8

		headerHeight := lipgloss.Height(m.list.Title)
		listFooterHeight := 2 // Pagination
		globalFooterHeight := 1

		// Adjust list height to fit global footer
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
			cmds = append(cmds, m.updateViewportContent(m.list.SelectedItem().(recipeItem).name))
		}

		if m.list.SelectedItem() != nil {
			cmds = append(cmds, m.updateViewportContent(m.list.SelectedItem().(recipeItem).name))
		}

	case recipeContentMsg:
		content := string(msg)
		// Wrap content to fit viewport width to prevent UI breakage
		if m.viewport.Width > 0 {
			content = lipgloss.NewStyle().Width(m.viewport.Width).Render(content)
		}
		m.viewport.SetContent(content)

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
		// Update all inputs
		for i := range m.inputs {
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
	if m.state == viewInput {
		content = m.inputView()
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
	}
	// Join with some spacing and styling. Ensure it spans full width or looks good.
	return helpStyle.Render(strings.Join(keys, " • "))
}

func (m model) inputView() string {
	var b strings.Builder

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
