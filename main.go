package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"

	"github.com/charmbracelet/bubbles/list"
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
	recipes        map[string]Recipe
	ready          bool
	err            error
	terminalWidth  int
	terminalHeight int
}

func main() {
	m := model{
		recipes: make(map[string]Recipe),
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
	m.list.SetShowHelp(false) // We'll show our own help or minimal help

	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
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
		// We need the sorted name, similar to main.
		// Since we don't store sorted items in model explicitly (only in list),
		// and we can't easily access list items by index in Init without logic duplication,
		// we'll rely on the WindowSizeMsg or the first Update to trigger it?
		// Better: just trigger a generic "refresh" or wait for first key/resize.
		// Actually, returning nil is fine, WindowSizeMsg usually comes first.
		// But to be safe, let's try to update the first one if possible.
		// Ideally we'd store the items in the model properly.
		// For now, let's just let the loop handle it or the user press down/up.
		// But let's try to grab the first item from the list model.
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
		case "ctrl+c", "q":
			// Only quit if not filtering
			if !m.list.SettingFilter() {
				return m, tea.Quit
			}

		case "enter":
			// Exec the selected task
			if i, ok := m.list.SelectedItem().(recipeItem); ok {
				return m, tea.ExecProcess(exec.Command("just", i.name), func(err error) tea.Msg {
					if err != nil {
						return fmt.Errorf("task failed: %v", err) // Simple error handling
					}
					return nil // Success
				})
			}
		}

	case tea.WindowSizeMsg:
		m.terminalWidth = msg.Width
		m.terminalHeight = msg.Height

		// Layout logic: Split 50/50? Or List 1/3, Viewport 2/3?
		// Let's do List on left (35%), Viewport on right (65%)
		listWidth := int(float64(msg.Width) * 0.35)
		viewportWidth := msg.Width - listWidth - 4 // minus padding/borders

		headerHeight := lipgloss.Height(m.list.Title)
		// Footer is generally 2 lines (pagination + help)
		footerHeight := 2

		m.list.SetSize(listWidth, msg.Height-headerHeight-footerHeight)

		if !m.ready {
			m.viewport = viewport.New(viewportWidth, msg.Height-2) // slightly smaller for borders
			m.viewport.HighPerformanceRendering = false
			m.ready = true
		} else {
			m.viewport.Width = viewportWidth
			m.viewport.Height = msg.Height - 2
		}

		// Trigger a content update for the current selection
		if m.list.SelectedItem() != nil {
			cmds = append(cmds, m.updateViewportContent(m.list.SelectedItem().(recipeItem).name))
		}

	case recipeContentMsg:
		m.viewport.SetContent(string(msg))

	case error:
		m.err = msg
		return m, nil
	}

	// Capture previous selection to detect change
	prevItem := m.list.SelectedItem()

	m.list, cmd = m.list.Update(msg)
	cmds = append(cmds, cmd)

	// Check if selection changed
	currItem := m.list.SelectedItem()
	if currItem != nil && (prevItem == nil || prevItem.(recipeItem).name != currItem.(recipeItem).name) {
		cmds = append(cmds, m.updateViewportContent(currItem.(recipeItem).name))
	}

	// If the window was resized, we need to make sure we update content too
	if _, ok := msg.(tea.WindowSizeMsg); ok && currItem != nil {
		cmds = append(cmds, m.updateViewportContent(currItem.(recipeItem).name))
	}

	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

func (m model) updateViewportContent(recipeName string) tea.Cmd {
	return func() tea.Msg {
		// Get detailed info
		// We force color to make it look nice in the viewport
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

	// Layout
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
