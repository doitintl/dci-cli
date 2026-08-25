package main

// F5 (TUI-SPEC): the interactive results viewer behind --table-mode
// interactive. A full-screen bubbles table over the same rows the static
// renderer receives: horizontal column scrolling (the wide-anomalies fix),
// column-aware sorting, client-side row filtering, and enter-prints-the-id
// selection. The UI renders on stderr in the alternate screen; stdout stays
// clean until the selection is printed on exit. Kept in a sibling file per
// the AGENTS.md chapter-split guidance.

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	viewerMinColWidth = 4
	viewerMaxColWidth = 32
	viewerChromeRows  = 5 // help bar, filter line, borders
)

type tableViewerModel struct {
	keys    []string
	rows    []map[string]interface{}
	cells   [][]string // formatted text, row-major, aligned with keys
	widths  []int      // content width per column
	visible []int      // row indices after filter, in sort order

	table     table.Model
	colOffset int
	focusCol  int
	sortCol   int
	sortAsc   bool
	filter    string
	filtering bool
	width     int
	height    int
	selection string
	idKey     string
}

// runTableViewer is a var so tests can fake the terminal program.
var runTableViewer = runTableViewerProgram

func runTableViewerProgram(rows []map[string]interface{}, keys []string) (string, error) {
	model := newTableViewerModel(rows, keys)
	// The alternate screen is declared per-frame in View (v2's declarative
	// model); only the stderr/stdin wiring remains a program option.
	program := tea.NewProgram(model, tea.WithOutput(os.Stderr), tea.WithInput(os.Stdin))
	final, err := program.Run()
	if err != nil {
		return "", fmt.Errorf("interactive table viewer: %w", err)
	}
	if viewer, ok := final.(*tableViewerModel); ok {
		return viewer.selection, nil
	}
	return "", nil
}

func newTableViewerModel(rows []map[string]interface{}, keys []string) *tableViewerModel {
	model := &tableViewerModel{
		keys:    keys,
		rows:    rows,
		sortCol: -1,
		width:   80,
		height:  24,
	}
	if len(rows) > 0 {
		if _, ok := rows[0]["id"].(string); ok {
			model.idKey = "id"
		}
	}
	model.cells = make([][]string, len(rows))
	model.widths = make([]int, len(keys))
	for i, key := range keys {
		model.widths[i] = len([]rune(key)) + 2 // room for the focus/sort marker
	}
	for rowIndex, row := range rows {
		cells := make([]string, len(keys))
		for colIndex, key := range keys {
			text := renderCellText(row, key)
			cells[colIndex] = text
			if width := len([]rune(text)); width > model.widths[colIndex] {
				model.widths[colIndex] = width
			}
		}
		model.cells[rowIndex] = cells
	}
	for i := range model.widths {
		if model.widths[i] > viewerMaxColWidth {
			model.widths[i] = viewerMaxColWidth
		}
		if model.widths[i] < viewerMinColWidth {
			model.widths[i] = viewerMinColWidth
		}
	}
	model.table = table.New(table.WithFocused(true))
	model.applyFilterAndSort()
	model.rebuild()
	return model
}

func (m *tableViewerModel) Init() tea.Cmd { return nil }

// visibleColumnRange returns the half-open column window that fits the
// terminal starting at colOffset.
func (m *tableViewerModel) visibleColumnRange() (int, int) {
	budget := m.width - 4
	end := m.colOffset
	for end < len(m.keys) {
		next := m.widths[end] + 2
		if budget-next < 0 && end > m.colOffset {
			break
		}
		budget -= next
		end++
	}
	return m.colOffset, end
}

// ensureFocusVisible shifts the column window until focusCol is inside it.
func (m *tableViewerModel) ensureFocusVisible() {
	if m.focusCol < m.colOffset {
		m.colOffset = m.focusCol
		return
	}
	for {
		_, end := m.visibleColumnRange()
		if m.focusCol < end || m.colOffset >= len(m.keys)-1 {
			return
		}
		m.colOffset++
	}
}

func (m *tableViewerModel) applyFilterAndSort() {
	m.visible = m.visible[:0]
	needle := strings.ToLower(strings.TrimSpace(m.filter))
	for rowIndex := range m.rows {
		if needle == "" {
			m.visible = append(m.visible, rowIndex)
			continue
		}
		for _, cell := range m.cells[rowIndex] {
			if strings.Contains(strings.ToLower(cell), needle) {
				m.visible = append(m.visible, rowIndex)
				break
			}
		}
	}
	if m.sortCol >= 0 && m.sortCol < len(m.keys) {
		key := m.keys[m.sortCol]
		sort.SliceStable(m.visible, func(i, j int) bool {
			less := viewerCellLess(m.rows[m.visible[i]][key], m.rows[m.visible[j]][key])
			if m.sortAsc {
				return less
			}
			return viewerCellLess(m.rows[m.visible[j]][key], m.rows[m.visible[i]][key])
		})
	}
}

// viewerCellLess orders raw cell values: numerically when both sides are
// numeric (money and epoch values included), lexically otherwise.
func viewerCellLess(a, b interface{}) bool {
	numericA, okA := numericCell(a)
	numericB, okB := numericCell(b)
	if okA && okB {
		return numericA < numericB
	}
	if okA != okB {
		return okA // numbers group before text
	}
	return strings.ToLower(formatTableValue(a)) < strings.ToLower(formatTableValue(b))
}

func (m *tableViewerModel) rebuild() {
	start, end := m.visibleColumnRange()
	columns := make([]table.Column, 0, end-start)
	for colIndex := start; colIndex < end; colIndex++ {
		title := m.keys[colIndex]
		if colIndex == m.sortCol {
			if m.sortAsc {
				title += " ▲"
			} else {
				title += " ▼"
			}
		}
		if colIndex == m.focusCol {
			title = "‣" + title
		}
		columns = append(columns, table.Column{Title: title, Width: m.widths[colIndex]})
	}
	tableRows := make([]table.Row, len(m.visible))
	for i, rowIndex := range m.visible {
		tableRows[i] = table.Row(m.cells[rowIndex][start:end])
	}
	cursor := m.table.Cursor()
	m.table.SetRows(nil)
	m.table.SetColumns(columns)
	m.table.SetRows(tableRows)
	if cursor >= len(tableRows) {
		cursor = len(tableRows) - 1
	}
	if cursor < 0 {
		cursor = 0
	}
	m.table.SetCursor(cursor)
	m.table.SetWidth(m.width - 2)
	height := m.height - viewerChromeRows
	if height < 3 {
		height = 3
	}
	m.table.SetHeight(height)
}

func (m *tableViewerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.rebuild()
		return m, nil
	case tea.KeyPressMsg:
		if m.filtering {
			switch {
			case msg.Code == tea.KeyEnter, msg.Code == tea.KeyEscape:
				if msg.Code == tea.KeyEscape {
					m.filter = ""
				}
				m.filtering = false
			case msg.Code == tea.KeyBackspace:
				if runes := []rune(m.filter); len(runes) > 0 {
					m.filter = string(runes[:len(runes)-1])
				}
			case msg.String() == "ctrl+c":
				return m, tea.Quit
			case msg.Text != "": // typed characters, space included
				m.filter += msg.Text
			}
			m.applyFilterAndSort()
			m.rebuild()
			return m, nil
		}
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "enter":
			if len(m.visible) > 0 {
				row := m.rows[m.visible[m.table.Cursor()]]
				if m.idKey != "" {
					m.selection, _ = row[m.idKey].(string)
				} else if len(m.keys) > 0 {
					m.selection = renderCellText(row, m.keys[0])
				}
			}
			return m, tea.Quit
		case "left", "h":
			if m.focusCol > 0 {
				m.focusCol--
				m.ensureFocusVisible()
				m.rebuild()
			}
			return m, nil
		case "right", "l":
			if m.focusCol < len(m.keys)-1 {
				m.focusCol++
				m.ensureFocusVisible()
				m.rebuild()
			}
			return m, nil
		case "s":
			if m.sortCol == m.focusCol {
				if m.sortAsc {
					m.sortAsc = false
				} else {
					m.sortCol = -1 // third press restores original order
				}
			} else {
				m.sortCol = m.focusCol
				m.sortAsc = true
			}
			m.applyFilterAndSort()
			m.rebuild()
			return m, nil
		case "/":
			m.filtering = true
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m *tableViewerModel) View() tea.View {
	status := fmt.Sprintf("%d/%d rows", len(m.visible), len(m.rows))
	if m.filter != "" {
		status += fmt.Sprintf(" · filter: %q", m.filter)
	}
	if start, end := m.visibleColumnRange(); start > 0 || end < len(m.keys) {
		status += fmt.Sprintf(" · columns %d–%d of %d", start+1, end, len(m.keys))
	}
	filterLine := ""
	if m.filtering {
		filterLine = "/" + m.filter + "▌"
	}
	help := "↑↓ rows · ←→ columns · s sort · / filter · enter select · q quit"
	if m.idKey == "" {
		help = strings.Replace(help, "enter select", "enter print first column", 1)
	}
	parts := []string{m.table.View(), tuiDimStyle.Render(status)}
	if filterLine != "" {
		parts = append(parts, filterLine)
	}
	parts = append(parts, tuiDimStyle.Render(help))
	view := tea.NewView(lipgloss.JoinVertical(lipgloss.Left, parts...))
	view.AltScreen = true
	return view
}
