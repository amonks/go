// Package datagrid renders semantic HTML tables with an optional client-side
// data-grid experience: full-text search, faceted filters, stable sorting,
// pagination, and shareable query-string state.
//
// Include Head in the document head once, then render one or more Table
// components. The package deliberately uses inline, embedded assets so apps do
// not need an asset handler or a mount-point convention.
package datagrid

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/a-h/templ"
)

// Feature identifies behavior that can be disabled globally or per column.
type Feature uint16

const (
	FeatureSearch Feature = 1 << iota
	FeatureSort
	FeatureFilters
	FeaturePagination
	FeatureURLState
)

const allFeatures = FeatureSearch | FeatureSort | FeatureFilters | FeaturePagination | FeatureURLState

// SortKind controls how browser values are compared. SortText uses a
// locale-aware natural comparison, so e.g. "item 2" sorts before "item 10".
type SortKind string

const (
	SortText    SortKind = "text"
	SortNumber  SortKind = "number"
	SortTime    SortKind = "time"
	SortBoolean SortKind = "boolean"
)

// FilterUI selects the filter presentation. FilterAuto shows a searchable
// checklist once cardinality reaches Options.FilterTypeaheadAt.
type FilterUI string

const (
	FilterAuto      FilterUI = "auto"
	FilterDropdown  FilterUI = "menu"
	FilterMenu      FilterUI = FilterDropdown
	FilterTypeahead FilterUI = "typeahead"
	FilterNone      FilterUI = "none"
)

// Theme controls color-scheme selection for one grid. ThemeAuto inherits the
// document's scheme, including any OS preference the document advertises.
type Theme string

// Panel places one grid's controls. PanelAuto is the container query's
// choice: a side rail when the grid is wide, a top bar when it is
// narrow. PanelTop keeps the top bar at every width, for a table whose
// columns want the width more than its two facets want a column of
// their own.
type Panel string

const (
	ThemeAuto  Theme = ""
	ThemeLight Theme = "light"
	ThemeDark  Theme = "dark"

	PanelAuto Panel = ""
	PanelTop  Panel = "top"
)

// FilterValue separates the stable query-string value from its human label.
// Label defaults to Value when empty.
type FilterValue struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Column describes one semantic table column and its client-side projections.
// Text is required and is the default display, search, sort, and one-valued
// filter projection. The optional functions override those roles independently.
type Column[T any] struct {
	Key   string
	Label string
	Text  func(T) string

	// Cell replaces the visible plain-text cell with an arbitrary templ
	// component. Behavioral projections still come from Text and the explicit
	// overrides below, never by scraping custom markup.
	Cell func(T) templ.Component
	// RowHeader renders this column's body cells as semantic row headers.
	RowHeader bool

	// Header replaces the visible header label while preserving the library's
	// sortable button and accessibility semantics. It must not itself contain
	// interactive descendants when sorting is enabled.
	Header templ.Component

	SearchText   func(T) string
	SortValue    func(T) string
	FilterValues func(T) []FilterValue

	SortKind SortKind
	FilterUI FilterUI
	Disabled Feature

	HeaderClass string
	CellClass   func(T) string
	// Align accepts "start", "center", or "end". Numeric columns default to
	// end alignment.
	Align string
}

// Options configures a full-data Table. ID is required and doubles as the
// default query namespace ("dg." + ID).
type Options[T any] struct {
	ID      string
	Columns []Column[T]
	Caption string

	SearchPlaceholder string
	EmptyText         string
	// PageSize is the fixed rows-per-page count, 25 by default. Rows-per-page
	// is a caller decision made here, not user-adjustable state.
	PageSize          int
	FilterTypeaheadAt int
	InitialState      State
	Disabled          Feature
	QueryPrefix       string
	// ClientHooks is an ordered comma- or whitespace-separated list registered
	// through window.Datagrid.register.
	ClientHooks string
	Theme       Theme
	Panel       Panel

	RowID         func(T) string
	RowClass      func(T) string
	RowSearchText func(T) string
	Empty         templ.Component

	// ControlPanel replaces the built-in search and filter panel without
	// disabling their state or hook pipelines. Caller controls can drive the
	// element through setState. Children passed to Table remain supplemental
	// content and render after either panel.
	ControlPanel templ.Component
}

// TextColumn returns a column whose visible and behavioral value is text.
func TextColumn[T any](key, label string, text func(T) string) Column[T] {
	return Column[T]{Key: key, Label: label, Text: text, SortKind: SortText, FilterUI: FilterAuto}
}

// ShellProps configures the exported low-level Shell templ component. Shell is
// useful when rows arrive after the page: render an annotated semantic table as
// the direct child of the generated .datagrid-table-wrap, give it the ID
// ID+"-table", insert rows, then call element.refresh().
type ShellProps struct {
	ID                string
	Label             string
	SearchPlaceholder string
	EmptyText         string
	PageSize          int
	FilterTypeaheadAt int
	InitialState      State
	Disabled          Feature
	QueryPrefix       string
	ClientHooks       string
	Theme             Theme
	Panel             Panel
	// InitialRows is the number of rows present in the caller-owned table at
	// render time. It drives the progressive, pre-upgrade summary and empty
	// state; callers that later replace or append rows call element.refresh().
	InitialRows  int
	ControlPanel templ.Component
	Extra        templ.Component
	Empty        templ.Component
}

// HeaderCellProps configures an annotated table header for low-level grids.
type HeaderCellProps struct {
	Column       string
	Label        string
	SortKind     SortKind
	FilterUI     FilterUI
	Align        string
	Disabled     Feature
	Class        string
	Content      templ.Component
	InitialState State
}

// RowProps configures an annotated table row for low-level grids.
type RowProps struct {
	ID        string
	Search    string
	HasSearch bool
	Class     string
}

// CellProps configures an annotated table cell for low-level grids.
type CellProps struct {
	Column string
	Value  string
	// RowHeader renders a <th scope="row"> instead of a <td>. All behavioral
	// projections and arbitrary content remain unchanged.
	RowHeader bool

	// Projections fall back to Value when omitted. The Has fields distinguish
	// an intentional empty override from the zero value.
	SearchValue     string
	HasSearchValue  bool
	SortValue       string
	HasSortValue    bool
	FilterValues    []FilterValue
	HasFilterValues bool

	Class   string
	Align   string
	Content templ.Component
}

// ControlProps identifies the grid controlled by an exported UI primitive.
type ControlProps struct {
	GridID            string
	SearchPlaceholder string
	InitialRows       int
	EmptyText         string
	Empty             templ.Component
}

type tableView struct {
	Shell   ShellProps
	Caption string
	Headers []HeaderCellProps
	Rows    []rowView
}

type rowView struct {
	Props RowProps
	Cells []CellProps
}

type tableSettings struct {
	pageSize    int
	typeaheadAt int
	queryPrefix string
	initial     State
	codec       QueryCodec
}

var slugPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)
var prefixPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]*$`)

// Table renders every row as HTML, then enhances it in place in the browser.
// A templ child passed to Table is rendered in the responsive controls panel,
// so caller controls move between the top bar and side rail with the grid.
// Configuration errors are returned when the component is rendered.
func Table[T any](opts Options[T], rows []T) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		view, err := makeTableView(opts, rows, templ.GetChildren(ctx))
		if err != nil {
			return fmt.Errorf("datagrid: %w", err)
		}
		return renderTable(view).Render(ctx, w)
	})
}

func makeTableView[T any](opts Options[T], rows []T, extra templ.Component) (tableView, error) {
	settings, err := settingsForOptions(opts)
	if err != nil {
		return tableView{}, err
	}

	searchPlaceholder := opts.SearchPlaceholder
	if searchPlaceholder == "" {
		searchPlaceholder = "Search rows…"
	}
	emptyText := opts.EmptyText
	if emptyText == "" {
		emptyText = "No rows match the current search and filters."
	}
	view := tableView{
		Caption: opts.Caption,
		Shell: ShellProps{
			ID:                opts.ID,
			Label:             opts.Caption,
			SearchPlaceholder: searchPlaceholder,
			EmptyText:         emptyText,
			PageSize:          settings.pageSize,
			FilterTypeaheadAt: settings.typeaheadAt,
			InitialState:      settings.initial,
			Disabled:          opts.Disabled,
			QueryPrefix:       settings.queryPrefix,
			ClientHooks:       opts.ClientHooks,
			Theme:             opts.Theme,
			Panel:             opts.Panel,
			InitialRows:       len(rows),
			ControlPanel:      opts.ControlPanel,
			Extra:             extra,
			Empty:             opts.Empty,
		},
		Headers: make([]HeaderCellProps, 0, len(opts.Columns)),
		Rows:    make([]rowView, 0, len(rows)),
	}
	if view.Shell.Label == "" {
		view.Shell.Label = "Data table"
	}
	for _, column := range opts.Columns {
		sortKind := column.SortKind
		if sortKind == "" {
			sortKind = SortText
		}
		filterUI := column.FilterUI
		if filterUI == "" {
			filterUI = FilterAuto
		}
		view.Headers = append(view.Headers, HeaderCellProps{
			Column:       column.Key,
			Label:        column.Label,
			SortKind:     sortKind,
			FilterUI:     filterUI,
			Align:        resolvedColumnAlign(column.Align, sortKind),
			Disabled:     column.Disabled | opts.Disabled,
			Class:        column.HeaderClass,
			Content:      column.Header,
			InitialState: settings.initial,
		})
	}

	rowIDs := make(map[string]struct{}, len(rows))
	for index, row := range rows {
		rowID := strconv.Itoa(index)
		if opts.RowID != nil {
			rowID = opts.RowID(row)
			if rowID == "" {
				return tableView{}, fmt.Errorf("row %d has an empty RowID", index)
			}
		}
		if _, exists := rowIDs[rowID]; exists {
			return tableView{}, fmt.Errorf("duplicate RowID %q", rowID)
		}
		rowIDs[rowID] = struct{}{}
		rowView := rowView{Props: RowProps{ID: rowID}, Cells: make([]CellProps, 0, len(opts.Columns))}
		if opts.RowClass != nil {
			rowView.Props.Class = opts.RowClass(row)
		}
		if opts.RowSearchText != nil {
			rowView.Props.Search = opts.RowSearchText(row)
			rowView.Props.HasSearch = true
		}
		for _, column := range opts.Columns {
			text := column.Text(row)
			var searchValue, sortValue string
			if column.SearchText != nil {
				searchValue = column.SearchText(row)
			}
			if column.SortValue != nil {
				sortValue = column.SortValue(row)
			}
			var filterValues []FilterValue
			if column.FilterValues != nil {
				filterValues = column.FilterValues(row)
			}
			cell := CellProps{
				Column:          column.Key,
				Value:           text,
				RowHeader:       column.RowHeader,
				SearchValue:     searchValue,
				HasSearchValue:  column.SearchText != nil,
				SortValue:       sortValue,
				HasSortValue:    column.SortValue != nil,
				FilterValues:    filterValues,
				HasFilterValues: column.FilterValues != nil,
				Align:           resolvedColumnAlign(column.Align, column.SortKind),
			}
			if column.CellClass != nil {
				cell.Class = column.CellClass(row)
			}
			if column.Cell != nil {
				cell.Content = column.Cell(row)
			}
			rowView.Cells = append(rowView.Cells, cell)
		}
		view.Rows = append(view.Rows, rowView)
	}
	return view, nil
}

func resolvedColumnAlign(align string, sortKind SortKind) string {
	if align != "" {
		return align
	}
	if sortKind == SortNumber {
		return "end"
	}
	return ""
}

// QueryCodecForOptions returns the server-side query codec that exactly
// matches Table's namespace, initial state, disabled features, and
// sortable/filterable column schema.
func QueryCodecForOptions[T any](opts Options[T]) (QueryCodec, error) {
	settings, err := settingsForOptions(opts)
	if err != nil {
		return QueryCodec{}, fmt.Errorf("datagrid: %w", err)
	}
	return settings.codec, nil
}

func settingsForOptions[T any](opts Options[T]) (tableSettings, error) {
	if err := validateOptions(opts); err != nil {
		return tableSettings{}, err
	}

	pageSize := opts.PageSize
	if pageSize == 0 {
		pageSize = 25
	}
	typeaheadAt := opts.FilterTypeaheadAt
	if typeaheadAt == 0 {
		typeaheadAt = 16
	}
	queryPrefix := opts.QueryPrefix
	if queryPrefix == "" {
		queryPrefix = "dg." + opts.ID
	}

	allColumns := columnKeys(opts.Columns)
	sortableColumns := make([]string, 0, len(opts.Columns))
	filterableColumns := make([]string, 0, len(opts.Columns))
	for _, column := range opts.Columns {
		if opts.Disabled&FeatureSort == 0 && column.Disabled&FeatureSort == 0 {
			sortableColumns = append(sortableColumns, column.Key)
		}
		if opts.Disabled&FeatureFilters == 0 && column.Disabled&FeatureFilters == 0 && column.FilterUI != FilterNone {
			filterableColumns = append(filterableColumns, column.Key)
		}
	}
	codec := QueryCodec{
		Prefix:            queryPrefix,
		Columns:           allColumns,
		SortableColumns:   sortableColumns,
		FilterableColumns: filterableColumns,
		Disabled:          opts.Disabled,
	}
	initial := codec.Normalize(opts.InitialState)
	codec.Defaults = initial
	return tableSettings{
		pageSize:    pageSize,
		typeaheadAt: typeaheadAt,
		queryPrefix: queryPrefix,
		initial:     initial,
		codec:       codec,
	}, nil
}

func validateOptions[T any](opts Options[T]) error {
	if !slugPattern.MatchString(opts.ID) {
		return fmt.Errorf("ID %q must match %s", opts.ID, slugPattern)
	}
	if len(opts.Columns) == 0 {
		return fmt.Errorf("at least one column is required")
	}
	if opts.PageSize < 0 || uint64(opts.PageSize) > maxJavaScriptInteger {
		return fmt.Errorf("PageSize must be between zero and %d", maxJavaScriptInteger)
	}
	if opts.FilterTypeaheadAt < 0 || uint64(opts.FilterTypeaheadAt) > maxJavaScriptInteger {
		return fmt.Errorf("FilterTypeaheadAt must be between zero and %d", maxJavaScriptInteger)
	}
	if opts.Disabled&^allFeatures != 0 {
		return fmt.Errorf("disabled features contain unknown flags")
	}
	if opts.QueryPrefix != "" && !prefixPattern.MatchString(opts.QueryPrefix) {
		return fmt.Errorf("QueryPrefix %q contains unsupported characters", opts.QueryPrefix)
	}
	if !validHookNames(opts.ClientHooks) {
		return fmt.Errorf("ClientHooks %q must contain only comma- or space-separated slugs", opts.ClientHooks)
	}
	if opts.Theme != ThemeAuto && opts.Theme != ThemeLight && opts.Theme != ThemeDark {
		return fmt.Errorf("unknown Theme %q", opts.Theme)
	}
	if opts.Panel != PanelAuto && opts.Panel != PanelTop {
		return fmt.Errorf("unknown Panel %q", opts.Panel)
	}
	seen := make(map[string]struct{}, len(opts.Columns))
	for index, column := range opts.Columns {
		if !slugPattern.MatchString(column.Key) {
			return fmt.Errorf("column %d key %q must match %s", index, column.Key, slugPattern)
		}
		if _, exists := seen[column.Key]; exists {
			return fmt.Errorf("duplicate column key %q", column.Key)
		}
		seen[column.Key] = struct{}{}
		if column.Label == "" {
			return fmt.Errorf("column %q has an empty Label", column.Key)
		}
		if column.Text == nil {
			return fmt.Errorf("column %q requires Text", column.Key)
		}
		if column.SortKind != "" && column.SortKind != SortText && column.SortKind != SortNumber && column.SortKind != SortTime && column.SortKind != SortBoolean {
			return fmt.Errorf("column %q has unknown SortKind %q", column.Key, column.SortKind)
		}
		if column.FilterUI != "" && column.FilterUI != FilterAuto && column.FilterUI != FilterMenu && column.FilterUI != FilterTypeahead && column.FilterUI != FilterNone {
			return fmt.Errorf("column %q has unknown FilterUI %q", column.Key, column.FilterUI)
		}
		if column.Disabled&^(FeatureSearch|FeatureSort|FeatureFilters) != 0 {
			return fmt.Errorf("column %q disables an unsupported feature", column.Key)
		}
		if column.Align != "" && column.Align != "start" && column.Align != "center" && column.Align != "end" {
			return fmt.Errorf("column %q has unsupported Align %q", column.Key, column.Align)
		}
	}
	if opts.Disabled&FeatureSearch != 0 && opts.InitialState.Search != "" {
		return fmt.Errorf("initial search requires search to be enabled")
	}
	if opts.Disabled&FeaturePagination != 0 && opts.InitialState.Page > 1 {
		return fmt.Errorf("initial page requires pagination to be enabled")
	}
	if opts.InitialState.Sort != "" {
		index := slices.IndexFunc(opts.Columns, func(column Column[T]) bool { return column.Key == opts.InitialState.Sort })
		if index < 0 || opts.Disabled&FeatureSort != 0 || opts.Columns[index].Disabled&FeatureSort != 0 {
			return fmt.Errorf("initial sort column %q is unknown or not sortable", opts.InitialState.Sort)
		}
	}
	for column := range opts.InitialState.Filters {
		index := slices.IndexFunc(opts.Columns, func(candidate Column[T]) bool { return candidate.Key == column })
		if index < 0 || opts.Disabled&FeatureFilters != 0 || opts.Columns[index].Disabled&FeatureFilters != 0 || opts.Columns[index].FilterUI == FilterNone {
			return fmt.Errorf("initial filter column %q is unknown or not filterable", column)
		}
	}
	return nil
}

func validHookNames(value string) bool {
	if value == "" {
		return true
	}
	names := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	if len(names) == 0 {
		return false
	}
	for _, name := range names {
		if !slugPattern.MatchString(name) {
			return false
		}
	}
	return true
}

func columnKeys[T any](columns []Column[T]) []string {
	keys := make([]string, len(columns))
	for i, column := range columns {
		keys[i] = column.Key
	}
	return keys
}

func featuresAttribute(disabled Feature) string {
	features := []struct {
		flag Feature
		name string
	}{
		{FeatureSearch, "search"},
		{FeatureSort, "sort"},
		{FeatureFilters, "filters"},
		{FeaturePagination, "pagination"},
		{FeatureURLState, "query"},
	}
	var enabled []string
	for _, feature := range features {
		if disabled&feature.flag == 0 {
			enabled = append(enabled, feature.name)
		}
	}
	return strings.Join(enabled, " ")
}

func stateJSON(state State) string {
	data, _ := json.Marshal(state)
	return string(data)
}

func filterValuesJSON(values []FilterValue) string {
	normalized := slices.Clone(values)
	if normalized == nil {
		normalized = []FilterValue{}
	}
	for i := range normalized {
		if normalized[i].Label == "" {
			normalized[i].Label = normalized[i].Value
		}
	}
	data, _ := json.Marshal(normalized)
	return string(data)
}

func sortKindValue(kind SortKind) string {
	if kind == "" {
		return string(SortText)
	}
	return string(kind)
}

func filterUIValue(ui FilterUI, disabled Feature) string {
	if disabled&FeatureFilters != 0 || ui == FilterNone {
		return string(FilterNone)
	}
	if ui == "" {
		return string(FilterAuto)
	}
	return string(ui)
}

func normalizeShellProps(props ShellProps) ShellProps {
	if props.Label == "" {
		props.Label = "Data table"
	}
	if props.SearchPlaceholder == "" {
		props.SearchPlaceholder = "Search rows…"
	}
	if props.EmptyText == "" {
		props.EmptyText = "No rows match the current search and filters."
	}
	if props.PageSize <= 0 || uint64(props.PageSize) > maxJavaScriptInteger {
		props.PageSize = 25
	}
	if props.FilterTypeaheadAt <= 0 || uint64(props.FilterTypeaheadAt) > maxJavaScriptInteger {
		props.FilterTypeaheadAt = 16
	}
	if props.QueryPrefix == "" {
		props.QueryPrefix = "dg." + props.ID
	}
	props.InitialState = (QueryCodec{
		Prefix:   props.QueryPrefix,
		Disabled: props.Disabled,
	}).Normalize(props.InitialState)
	return props
}

func initialAriaSort(props HeaderCellProps) string {
	if props.Disabled&FeatureSort != 0 {
		return ""
	}
	if props.InitialState.Sort != props.Column {
		return "none"
	}
	if props.InitialState.Descending {
		return "descending"
	}
	return "ascending"
}

func initialSummary(rows int) string {
	if rows == 1 {
		return "1 row"
	}
	return strconv.Itoa(rows) + " rows"
}

func hasPanel(props ShellProps) bool {
	return props.ControlPanel != nil || props.Disabled&FeatureSearch == 0 || props.Disabled&FeatureFilters == 0 || props.Extra != nil
}
