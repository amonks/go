package datagrid

import (
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

const maxJavaScriptInteger uint64 = 1<<53 - 1

// State is the shareable state of one grid. Page numbers are one-based.
// Filters are ORed within a column and ANDed between columns.
type State struct {
	Search      string              `json:"search,omitempty"`
	Filters     map[string][]string `json:"filters,omitempty"`
	Sort        string              `json:"sort,omitempty"`
	Descending  bool                `json:"descending,omitempty"`
	Page        int                 `json:"page,omitempty"`
	RowsPerPage int                 `json:"rowsPerPage,omitempty"`
}

// QueryCodec reads and writes a grid's state without disturbing any other
// query parameters. Prefix should be stable and unique on the page, normally
// "dg." followed by the grid's ID.
//
// Columns restricts both sort and filter keys. SortableColumns and
// FilterableColumns override that shared list for their respective roles; a
// nonnil empty role list disables that role. A nil Columns slice accepts any
// syntactically valid column key. PageSizes restricts RowsPerPage when it is
// nonempty. Disabled mirrors feature normalization in the browser.
type QueryCodec struct {
	Prefix            string
	Defaults          State
	Columns           []string
	SortableColumns   []string
	FilterableColumns []string
	PageSizes         []int
	Disabled          Feature
}

// Normalize returns the canonical, schema-valid form of state.
func (c QueryCodec) Normalize(state State) State {
	defaults := c.normalize(c.Defaults, 0)
	result := c.normalize(state, defaults.RowsPerPage)
	if c.Disabled&FeaturePagination != 0 {
		result.RowsPerPage = defaults.RowsPerPage
	}
	return result
}

// Decode parses the state owned by this codec. If the query contains no
// recognized parameter in this codec's namespace, Decode returns Defaults.
// Malformed values are ignored.
func (c QueryCodec) Decode(query url.Values) State {
	defaults := c.Normalize(c.Defaults)
	if !c.hasState(query) {
		return cloneState(defaults)
	}

	state := State{
		Page:        1,
		RowsPerPage: defaults.RowsPerPage,
		Filters:     make(map[string][]string),
	}
	state.Search = query.Get(c.key("search"))
	state.Sort = query.Get(c.key("sort"))
	state.Descending = query.Get(c.key("dir")) == "desc"
	if page, err := strconv.Atoi(query.Get(c.key("page"))); err == nil {
		state.Page = page
	}
	if pageSize, err := strconv.Atoi(query.Get(c.key("per-page"))); err == nil {
		state.RowsPerPage = pageSize
	}

	filterPrefix := c.key("filter.")
	for key, values := range query {
		if !strings.HasPrefix(key, filterPrefix) {
			continue
		}
		column := strings.TrimPrefix(key, filterPrefix)
		if column == "" || !c.validFilterColumn(column) {
			continue
		}
		state.Filters[column] = slices.Clone(values)
	}
	return c.Normalize(state)
}

// Encode replaces only this codec's parameters in query. Defaults are
// represented by the absence of the namespace. Encode mutates query in place.
func (c QueryCodec) Encode(query url.Values, state State) {
	if query == nil {
		return
	}
	prefix := c.Prefix + "."
	for key := range query {
		if strings.HasPrefix(key, prefix) {
			delete(query, key)
		}
	}
	if c.Disabled&FeatureURLState != 0 {
		return
	}

	state = c.Normalize(state)
	defaults := c.Normalize(c.Defaults)
	if statesEqual(state, defaults) {
		return
	}

	// The marker distinguishes an explicitly cleared state from a URL with no
	// state at all, which means Defaults (and may itself be nonempty).
	query.Set(c.key("state"), "1")
	if c.Disabled&FeatureSearch == 0 && state.Search != "" {
		query.Set(c.key("search"), state.Search)
	}
	if c.Disabled&FeatureSort == 0 && state.Sort != "" {
		query.Set(c.key("sort"), state.Sort)
		if state.Descending {
			query.Set(c.key("dir"), "desc")
		} else {
			query.Set(c.key("dir"), "asc")
		}
	}
	if c.Disabled&FeaturePagination == 0 && state.Page > 1 {
		query.Set(c.key("page"), strconv.Itoa(state.Page))
	}
	if c.Disabled&FeaturePagination == 0 && state.RowsPerPage > 0 && state.RowsPerPage != defaults.RowsPerPage {
		query.Set(c.key("per-page"), strconv.Itoa(state.RowsPerPage))
	}
	columns := make([]string, 0, len(state.Filters))
	for column := range state.Filters {
		columns = append(columns, column)
	}
	slices.Sort(columns)
	for _, column := range columns {
		query[c.key("filter."+column)] = slices.Clone(state.Filters[column])
	}
}

func (c QueryCodec) normalize(state State, fallbackPageSize int) State {
	result := State{
		Search:      state.Search,
		Sort:        state.Sort,
		Descending:  state.Descending,
		Page:        state.Page,
		RowsPerPage: state.RowsPerPage,
		Filters:     make(map[string][]string),
	}
	if c.Disabled&FeatureSearch != 0 {
		result.Search = ""
	}
	if c.Disabled&FeaturePagination != 0 || result.Page < 1 || uint64(result.Page) > maxJavaScriptInteger {
		result.Page = 1
	}
	if result.RowsPerPage <= 0 || uint64(result.RowsPerPage) > maxJavaScriptInteger || !c.validPageSize(result.RowsPerPage) {
		result.RowsPerPage = fallbackPageSize
		if result.RowsPerPage <= 0 || !c.validPageSize(result.RowsPerPage) {
			result.RowsPerPage = 0
			for _, pageSize := range c.PageSizes {
				if c.validPageSize(pageSize) {
					result.RowsPerPage = pageSize
					break
				}
			}
		}
	}
	if c.Disabled&FeatureSort != 0 || result.Sort == "" || !c.validSortColumn(result.Sort) {
		result.Sort = ""
		result.Descending = false
	}
	for column, values := range state.Filters {
		if c.Disabled&FeatureFilters != 0 || !c.validFilterColumn(column) {
			continue
		}
		values = slices.Clone(values)
		slices.Sort(values)
		values = slices.Compact(values)
		if len(values) > 0 {
			result.Filters[column] = values
		}
	}
	return result
}

func (c QueryCodec) hasState(query url.Values) bool {
	if c.Disabled&FeatureURLState != 0 {
		return false
	}
	suffixes := []string{"state"}
	if c.Disabled&FeatureSearch == 0 {
		suffixes = append(suffixes, "search")
	}
	if c.Disabled&FeatureSort == 0 {
		suffixes = append(suffixes, "sort", "dir")
	}
	if c.Disabled&FeaturePagination == 0 {
		suffixes = append(suffixes, "page", "per-page")
	}
	for _, suffix := range suffixes {
		if _, ok := query[c.key(suffix)]; ok {
			return true
		}
	}
	filterPrefix := c.key("filter.")
	for key := range query {
		if c.Disabled&FeatureFilters == 0 && strings.HasPrefix(key, filterPrefix) && c.validFilterColumn(strings.TrimPrefix(key, filterPrefix)) {
			return true
		}
	}
	return false
}

func (c QueryCodec) validSortColumn(column string) bool {
	return c.validColumnForRole(column, c.SortableColumns)
}

func (c QueryCodec) validFilterColumn(column string) bool {
	return c.validColumnForRole(column, c.FilterableColumns)
}

func (c QueryCodec) validColumnForRole(column string, role []string) bool {
	if !slugPattern.MatchString(column) {
		return false
	}
	if role != nil {
		return slices.Contains(role, column)
	}
	return c.Columns == nil || slices.Contains(c.Columns, column)
}

func (c QueryCodec) validPageSize(pageSize int) bool {
	return pageSize > 0 && uint64(pageSize) <= maxJavaScriptInteger && (len(c.PageSizes) == 0 || slices.Contains(c.PageSizes, pageSize))
}

func (c QueryCodec) key(suffix string) string {
	return c.Prefix + "." + suffix
}

func cloneState(state State) State {
	result := state
	result.Filters = make(map[string][]string, len(state.Filters))
	for column, values := range state.Filters {
		result.Filters[column] = slices.Clone(values)
	}
	return result
}

func statesEqual(a, b State) bool {
	return a.Search == b.Search &&
		a.Sort == b.Sort &&
		a.Descending == b.Descending &&
		a.Page == b.Page &&
		a.RowsPerPage == b.RowsPerPage &&
		maps.EqualFunc(a.Filters, b.Filters, slices.Equal)
}
