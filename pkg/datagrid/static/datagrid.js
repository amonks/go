(() => {
  "use strict";

  /*
   * Custom behavior is registered with Datagrid.register(name, hooks), then
   * selected by a grid's whitespace-separated data-dg-hooks attribute.
   *
   * Every hook receives one context object. search and filter return a boolean;
   * compare returns a signed number in the requested direction. Returning
   * undefined from any hook asks the grid to use its built-in result, exposed
   * as context.defaultResult. Contexts also contain grid, row/state, and the
   * operation-specific values described where they are constructed below.
   * Hooks are synchronous. A later hook is tried only when an earlier hook
   * returns undefined.
   */

  const tagName = "monks-datagrid";
  const allFeatures = ["search", "sort", "filters", "pagination", "query"];
  // At or below this many rows the whole table is scannable, so controls
  // that exist to narrow it — auto facets, the search box — are noise.
  const scannableRowCount = 5;
  const disabledValues = new Set(["none", "false", "off", "disabled", "0"]);
  const naturalCollator = new Intl.Collator(undefined, {
    numeric: true,
    sensitivity: "base",
  });

  const api = window.Datagrid || {};
  const hookRegistry = api.__hookRegistry instanceof Map
    ? api.__hookRegistry
    : new Map();

  if (!(api.__hookRegistry instanceof Map)) {
    Object.defineProperty(api, "__hookRegistry", {
      configurable: false,
      enumerable: false,
      value: hookRegistry,
      writable: false,
    });
  }

  api.register = (name, hooks) => {
    if (typeof name !== "string" || name.trim() === "") {
      throw new TypeError("Datagrid.register requires a non-empty hook name");
    }
    if (hooks === null || typeof hooks !== "object") {
      throw new TypeError("Datagrid.register requires a hooks object");
    }
    for (const hookName of ["search", "filter", "compare"]) {
      if (hooks[hookName] !== undefined && typeof hooks[hookName] !== "function") {
        throw new TypeError(`Datagrid hook ${hookName} must be a function`);
      }
    }

    const key = name.trim();
    hookRegistry.set(key, hooks);
    return () => {
      if (hookRegistry.get(key) === hooks) {
        hookRegistry.delete(key);
      }
    };
  };

  window.Datagrid = api;

  if (customElements.get(tagName)) {
    return;
  }

  function parseJSON(value, fallback) {
    if (typeof value !== "string" || value === "") {
      return fallback;
    }
    try {
      return JSON.parse(value);
    } catch (_error) {
      return fallback;
    }
  }

  function positiveInteger(value) {
    const number = Number(value);
    return Number.isSafeInteger(number) && number > 0 ? number : 0;
  }

  function queryInteger(value) {
    if (typeof value !== "string" || !/^[+-]?\d+$/.test(value)) {
      return null;
    }
    const number = Number(value);
    return Number.isSafeInteger(number) ? number : null;
  }

  function normalizeText(value) {
    return String(value ?? "")
      .normalize("NFKD")
      .replace(/\p{M}/gu, "")
      .toLowerCase();
  }

  function tokenize(value) {
    const normalized = normalizeText(value).trim();
    return normalized === "" ? [] : normalized.split(/\s+/u);
  }

  function isEnabledValue(value, fallback = true) {
    if (value === null || value === undefined || value === "") {
      return fallback;
    }
    return !disabledValues.has(String(value).trim().toLowerCase());
  }

  function cloneState(state) {
    const filters = {};
    for (const [column, values] of Object.entries(state?.filters || {})) {
      filters[column] = [...values];
    }
    return {
      search: state?.search || "",
      filters,
      sort: state?.sort || "",
      descending: Boolean(state?.descending),
      page: state?.page || 1,
    };
  }

  function statesEqual(a, b) {
    if (
      a.search !== b.search ||
      a.sort !== b.sort ||
      a.descending !== b.descending ||
      a.page !== b.page
    ) {
      return false;
    }

    const aKeys = Object.keys(a.filters).sort(compareCanonicalStrings);
    const bKeys = Object.keys(b.filters).sort(compareCanonicalStrings);
    if (aKeys.length !== bKeys.length) {
      return false;
    }
    for (let index = 0; index < aKeys.length; index += 1) {
      if (aKeys[index] !== bKeys[index]) {
        return false;
      }
      const aValues = a.filters[aKeys[index]];
      const bValues = b.filters[bKeys[index]];
      if (
        aValues.length !== bValues.length ||
        aValues.some((value, valueIndex) => value !== bValues[valueIndex])
      ) {
        return false;
      }
    }
    return true;
  }

  // Go compares valid UTF-8 strings by Unicode scalar value. JavaScript's
  // default sort compares UTF-16 code units, which would produce a different
  // canonical query for some BMP and supplementary characters.
  function compareCanonicalStrings(aValue, bValue) {
    const a = String(aValue);
    const b = String(bValue);
    let aIndex = 0;
    let bIndex = 0;
    while (aIndex < a.length && bIndex < b.length) {
      const aPoint = a.codePointAt(aIndex);
      const bPoint = b.codePointAt(bIndex);
      if (aPoint !== bPoint) {
        return aPoint < bPoint ? -1 : 1;
      }
      aIndex += aPoint > 0xffff ? 2 : 1;
      bIndex += bPoint > 0xffff ? 2 : 1;
    }
    return aIndex < a.length ? 1 : bIndex < b.length ? -1 : 0;
  }

  function uniqueSortedStrings(values) {
    return [...new Set(values.map((value) => String(value)))]
      .sort(compareCanonicalStrings);
  }

  function pageItems(current, total) {
    if (total <= 7) {
      return Array.from({ length: total }, (_unused, index) => index + 1);
    }

    const pages = new Set([1, total, current - 1, current, current + 1]);
    if (current <= 4) {
      for (let page = 2; page <= 5; page += 1) {
        pages.add(page);
      }
    }
    if (current >= total - 3) {
      for (let page = total - 4; page < total; page += 1) {
        pages.add(page);
      }
    }

    const sorted = [...pages]
      .filter((page) => page >= 1 && page <= total)
      .sort((a, b) => a - b);
    const result = [];
    for (const page of sorted) {
      const previous = result[result.length - 1];
      if (typeof previous === "number" && page - previous > 1) {
        result.push("ellipsis");
      }
      result.push(page);
    }
    return result;
  }

  class DatagridElement extends HTMLElement {
    constructor() {
      super();
      this._connected = false;
      this._initialized = false;
      this._abortController = null;
      this._features = new Set();
      this._columns = new Map();
      this._records = [];
      this._state = null;
      this._defaults = null;
      this._originalPositions = new WeakMap();
      this._nextOriginalPosition = 0;
      this._hookState = null;
    }

    connectedCallback() {
      if (this._connected) {
        return;
      }
      this._connected = true;
      this._bindEvents();

      // The definition normally lives in <head>. During HTML parsing the
      // custom element is connected as soon as its opening tag is seen, before
      // its table and controls have been parsed. Wait for parsing to finish;
      // the microtask path also lets a caller append and then populate a grid
      // synchronously.
      const initialize = () => {
        queueMicrotask(() => {
          if (!this._connected) {
            return;
          }
          this._refresh({ readQuery: true, writeQuery: true });
          this.setAttribute("data-dg-ready", "");
        });
      };
      if (document.readyState === "loading") {
        document.addEventListener("DOMContentLoaded", initialize, {
          once: true,
          signal: this._abortController.signal,
        });
      } else {
        initialize();
      }
    }

    disconnectedCallback() {
      this._connected = false;
      this._abortController?.abort();
      this._abortController = null;
    }

    getState() {
      if (!this._state) {
        this._configure();
        this._defaults = this._readDefaults();
        this._state = cloneState(this._defaults);
      }
      return cloneState(this._state);
    }

    setState(state, options = {}) {
      this._ensurePrepared();
      this._setState(state, {
        emit: options.emit !== false,
        writeQuery: options.updateURL !== false,
        renderUnknowns: true,
      });
    }

    reset(options = {}) {
      this._ensurePrepared();
      this._setState(this._defaults, {
        emit: options.emit !== false,
        writeQuery: options.updateURL !== false,
        renderUnknowns: true,
      });
    }

    refresh() {
      const view = this._captureRefreshView();
      this._refresh({ readQuery: false, writeQuery: this._connected });
      this._restoreRefreshView(view);
    }

    _ensurePrepared() {
      if (!this._initialized) {
        this._refresh({ readQuery: false, writeQuery: false });
      }
    }

    _refresh({ readQuery, writeQuery }) {
      this._configure();
      this._defaults = this._readDefaults();

      if (!this._initialized) {
        this._state = cloneState(this._defaults);
        this._initialized = true;
      } else {
        this._state = this._normalizeState(this._state);
      }
      if (readQuery && this._features.has("query")) {
        this._state = this._decodeQuery();
      }

      this._collectRows();
      this._renderFilters();
      this._apply({ writeQuery });
    }

    _captureRefreshView() {
      const filters = new Map();
      for (const details of this._generatedFilterDetails()) {
        const column = details.getAttribute("data-dg-filter-column") || "";
        if (column === "") {
          continue;
        }
        const search = details.querySelector('[data-dg-role="filter-search"]');
        filters.set(column, {
          open: details.open,
          search: search instanceof HTMLInputElement ? search.value : "",
        });
      }

      const active = document.activeElement;
      if (!(active instanceof HTMLElement) || !this.contains(active) || !this._owns(active)) {
        return { filters, focus: null };
      }
      const attributes = {};
      for (const name of [
        "data-dg-role",
        "data-dg-filter-column",
        "data-dg-column",
        "data-dg-page",
        "data-dg-page-action",
        "type",
        "value",
      ]) {
        if (active.hasAttribute(name)) {
          attributes[name] = active.getAttribute(name) || "";
        }
      }
      const focus = {
        element: active,
        tagName: active.tagName.toLowerCase(),
        id: active.id,
        attributes,
        filterColumn: active.closest("details[data-dg-filter-column]")
          ?.getAttribute("data-dg-filter-column") || "",
      };
      if (active instanceof HTMLInputElement || active instanceof HTMLTextAreaElement) {
        focus.selectionStart = active.selectionStart;
        focus.selectionEnd = active.selectionEnd;
        focus.selectionDirection = active.selectionDirection;
      }
      return { filters, focus };
    }

    _restoreRefreshView(view) {
      if (!view) {
        return;
      }
      for (const [column, state] of view.filters) {
        const details = this._generatedFilterDetails()
          .find((candidate) => candidate.getAttribute("data-dg-filter-column") === column);
        if (!details) {
          continue;
        }
        details.open = state.open;
        const search = details.querySelector('[data-dg-role="filter-search"]');
        if (search instanceof HTMLInputElement) {
          search.value = state.search;
          this._filterLocalOptions(search);
        }
      }

      const bookmark = view.focus;
      if (!bookmark) {
        return;
      }
      let target = bookmark.element.isConnected && this.contains(bookmark.element)
        ? bookmark.element
        : null;
      if (!target && bookmark.id !== "") {
        const byID = document.getElementById(bookmark.id);
        if (byID instanceof HTMLElement && this.contains(byID) && this._owns(byID)) {
          target = byID;
        }
      }
      if (!target && (bookmark.filterColumn !== "" || Object.keys(bookmark.attributes).length > 0)) {
        target = this._findAllOwned(bookmark.tagName).find((candidate) => {
          if (!(candidate instanceof HTMLElement)) {
            return false;
          }
          for (const [name, value] of Object.entries(bookmark.attributes)) {
            if (!candidate.hasAttribute(name) || (candidate.getAttribute(name) || "") !== value) {
              return false;
            }
          }
          return (candidate.closest("details[data-dg-filter-column]")
            ?.getAttribute("data-dg-filter-column") || "") === bookmark.filterColumn;
        }) || null;
      }
      if (!target || target.closest("tr[data-dg-row]")?.hidden) {
        return;
      }
      if (document.activeElement !== target) {
        target.focus({ preventScroll: true });
      }
      if (
        bookmark.selectionStart !== null && bookmark.selectionStart !== undefined &&
        bookmark.selectionEnd !== null && bookmark.selectionEnd !== undefined &&
        (target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement)
      ) {
        target.setSelectionRange(
          bookmark.selectionStart,
          bookmark.selectionEnd,
          bookmark.selectionDirection || undefined,
        );
      }
    }

    _configure() {
      const featureAttribute = this.getAttribute("data-dg-features");
      this._features = new Set(
        featureAttribute === null
          ? allFeatures
          : featureAttribute.split(/[\s,]+/u).filter(Boolean),
      );

      this._pageSize = positiveInteger(this.getAttribute("data-dg-page-size"));
      this._typeaheadAt = positiveInteger(this.getAttribute("data-dg-typeahead-at")) || 16;
      this._queryPrefix = this.getAttribute("data-dg-query-prefix") ||
        (this.id ? `dg.${this.id}` : "dg.datagrid");

      const hookAttribute = this.getAttribute("data-dg-hooks") || "";
      const parsedHookNames = parseJSON(hookAttribute, null);
      this._hookNames = Array.isArray(parsedHookNames)
        ? parsedHookNames.map(String).filter(Boolean)
        : hookAttribute.split(/[\s,]+/u).filter(Boolean);

      this._table = this._findOwned(".datagrid-table-wrap > table");
      this._tbody = this._table
        ? [...this._table.querySelectorAll("tbody")].find((node) => this._owns(node)) || null
        : null;
      this._searchInput = this._findOwned('[data-dg-role="search"]');
      this._empty = this._findOwned('[data-dg-role="empty"]');
      this._filterHost = this._findOwned('[data-dg-role="filters"]');
      this._summary = this._findOwned('[data-dg-role="summary"]');
      this._pagination = this._findOwned('[data-dg-role="pagination"]');

      this._columns = new Map();
      const headers = this._table
        ? [...this._table.querySelectorAll("thead th[data-dg-column]")]
          .filter((node) => this._owns(node))
        : [];
      for (const header of headers) {
        const key = header.getAttribute("data-dg-column") || "";
        if (key === "" || this._columns.has(key)) {
          continue;
        }
        const sortButton = [...header.querySelectorAll('[data-dg-role="sort"]')]
          .find((node) => this._owns(node)) || null;
        const sortKind = (header.getAttribute("data-dg-sort-kind") || "text")
          .trim()
          .toLowerCase();
        const filterMode = header.getAttribute("data-dg-filter");
        const searchMode = header.getAttribute("data-dg-search");
        const label = header.getAttribute("data-dg-label") ||
          sortButton?.textContent?.trim() ||
          header.textContent?.trim() ||
          key;
        this._columns.set(key, {
          key,
          label,
          header,
          sortButton,
          sortKind,
          filterMode: (filterMode || "auto").trim().toLowerCase(),
          sortable:
            this._features.has("sort") &&
            Boolean(sortButton) &&
            isEnabledValue(sortKind),
          filterable:
            this._features.has("filters") && isEnabledValue(filterMode),
          searchable:
            this._features.has("search") && isEnabledValue(searchMode),
        });
      }
    }

    _generatedFilterDetails() {
      return this._findAllOwned(
        '[data-dg-generated-filters=""] > details[data-dg-filter-column]',
      );
    }

    _readDefaults() {
      const raw = parseJSON(this.getAttribute("data-dg-initial-state"), {});
      const initial = raw && typeof raw === "object" && !Array.isArray(raw) ? raw : {};
      return this._normalizeState(initial);
    }

    _normalizeState(input) {
      const candidate = input && typeof input === "object" ? input : {};
      const filters = {};
      if (this._features.has("filters") && candidate.filters && typeof candidate.filters === "object") {
        for (const [column, rawValues] of Object.entries(candidate.filters)) {
          if (!this._columns.get(column)?.filterable || !Array.isArray(rawValues)) {
            continue;
          }
          const values = uniqueSortedStrings(rawValues);
          if (values.length > 0) {
            filters[column] = values;
          }
        }
      }

      const requestedSort = candidate.sort == null ? "" : String(candidate.sort);
      const sort = this._columns.get(requestedSort)?.sortable ? requestedSort : "";
      const requestedPage = Number(candidate.page);
      const page = Number.isSafeInteger(requestedPage) && requestedPage > 0
        ? requestedPage
        : 1;

      return {
        search: this._features.has("search") && candidate.search != null
          ? String(candidate.search)
          : "",
        filters,
        sort,
        descending: sort !== "" && Boolean(candidate.descending),
        page: this._features.has("pagination") ? page : 1,
      };
    }

    _collectRows() {
      this._records = [];
      if (!this._tbody) {
        return;
      }

      const rows = [...this._tbody.querySelectorAll("tr[data-dg-row]")]
        .filter((row) => this._owns(row));
      for (const row of rows) {
        if (!this._originalPositions.has(row)) {
          this._originalPositions.set(row, this._nextOriginalPosition);
          this._nextOriginalPosition += 1;
        }

        const cells = new Map();
        for (const cell of row.querySelectorAll("td[data-dg-column], th[data-dg-column]")) {
          if (!this._owns(cell) || cell.closest("tr") !== row) {
            continue;
          }
          const key = cell.getAttribute("data-dg-column") || "";
          if (key !== "" && !cells.has(key)) {
            cells.set(key, cell);
          }
        }

        const values = new Map();
        const sortValues = new Map();
        const filterValues = new Map();
        const searchable = [];
        for (const column of this._columns.values()) {
          const cell = cells.get(column.key) || null;
          const value = this._cellValue(cell, "data-dg-value");
          values.set(column.key, value);
          sortValues.set(
            column.key,
            this._cellValue(cell, "data-dg-sort-value", value),
          );
          filterValues.set(column.key, this._cellFilterValues(cell, value));
          if (column.searchable) {
            searchable.push(this._cellValue(cell, "data-dg-search-value", value));
          }
        }

        const rowSearchText = row.hasAttribute("data-dg-search")
          ? row.getAttribute("data-dg-search") || ""
          : searchable.join(" ");
        this._records.push({
          element: row,
          index: this._originalPositions.get(row),
          cells,
          values,
          sortValues,
          filterValues,
          searchText: normalizeText(rowSearchText),
        });
      }
    }

    _cellValue(cell, attribute, fallback = "") {
      if (!cell) {
        return fallback;
      }
      if (cell.hasAttribute(attribute)) {
        return cell.getAttribute(attribute) || "";
      }
      if (attribute !== "data-dg-value" && cell.hasAttribute("data-dg-value")) {
        return cell.getAttribute("data-dg-value") || "";
      }
      return attribute === "data-dg-value"
        ? (cell.textContent || "").trim()
        : fallback;
    }

    _cellFilterValues(cell, fallback) {
      if (cell?.hasAttribute("data-dg-filter-values")) {
        const parsed = parseJSON(cell.getAttribute("data-dg-filter-values"), undefined);
        if (parsed === null) {
          return [];
        }
        if (Array.isArray(parsed)) {
          const result = [];
          const seen = new Set();
          for (const item of parsed) {
            const value = item && typeof item === "object" && "value" in item
              ? String(item.value ?? "")
              : String(item ?? "");
            if (seen.has(value)) {
              continue;
            }
            seen.add(value);
            const rawLabel = item && typeof item === "object" && "label" in item
              ? String(item.label ?? "")
              : value;
            result.push({ value, label: rawLabel || (value === "" ? "(Blank)" : value) });
          }
          return result;
        }
      }

      const value = String(fallback ?? "");
      return [{ value, label: value === "" ? "(Blank)" : value }];
    }

    _renderFilters() {
      const previous = this._filterHost?.querySelector('[data-dg-generated-filters=""]');
      if (previous && this._owns(previous)) {
        previous.remove();
      }
      if (!this._filterHost || !this._features.has("filters")) {
        return;
      }

      const columns = [...this._columns.values()].filter((column) => column.filterable);
      if (columns.length === 0) {
        return;
      }

      const wrapper = document.createElement("div");
      wrapper.className = "datagrid-generated-filters";
      wrapper.setAttribute("data-dg-generated-filters", "");

      if (!this._findOwned('[data-dg-role="clear"]')) {
        const clear = document.createElement("button");
        clear.type = "button";
        clear.className = "datagrid-filter-clear";
        clear.setAttribute("data-dg-role", "clear");
        clear.textContent = "Clear all";
        wrapper.append(clear);
      }

      for (const column of columns) {
        const optionsByValue = new Map();
        let narrows = false;
        for (const record of this._records) {
          for (const option of record.filterValues.get(column.key) || []) {
            if (optionsByValue.has(option.value)) {
              narrows = true;
            } else {
              optionsByValue.set(option.value, option);
            }
          }
        }
        const selected = this._state?.filters[column.key] || [];
        // An auto facet that cannot usefully narrow the table is noise:
        // with a scannable table (see scannableRowCount) there is nothing
        // to narrow, and a column where no value recurs (for a
        // single-valued column, cardinality equals the row count) only
        // re-selects individual rows. An active selection still renders
        // so it stays visible and removable, and an explicit menu or
        // typeahead column is the caller's decision either way.
        if (
          column.filterMode === "auto" &&
          selected.length === 0 &&
          (this._records.length <= scannableRowCount || !narrows)
        ) {
          continue;
        }
        for (const value of selected) {
          if (!optionsByValue.has(value)) {
            optionsByValue.set(value, {
              value,
              label: value === "" ? "(Blank)" : value,
            });
          }
        }
        const options = [...optionsByValue.values()].sort((a, b) => {
          const byLabel = naturalCollator.compare(a.label, b.label);
          return byLabel || naturalCollator.compare(a.value, b.value);
        });
        if (options.length === 0) {
          continue;
        }

        const details = document.createElement("details");
        details.className = "datagrid-filter";
        details.setAttribute("data-dg-filter-column", column.key);

        const heading = document.createElement("summary");
        heading.className = "datagrid-filter-heading";
        const headingLabel = document.createElement("span");
        headingLabel.className = "datagrid-filter-name";
        headingLabel.textContent = column.label;
        const selection = document.createElement("span");
        selection.className = "datagrid-filter-selection";
        selection.setAttribute("data-dg-filter-selection", "");
        heading.append(headingLabel, selection);
        details.append(heading);

        const searchableOptions =
          column.filterMode === "typeahead" ||
          (column.filterMode === "auto" && options.length >= this._typeaheadAt);
        if (searchableOptions) {
          const search = document.createElement("input");
          search.type = "search";
          search.autocomplete = "off";
          search.className = "datagrid-filter-search";
          search.setAttribute("data-dg-role", "filter-search");
          search.setAttribute("data-dg-filter-column", column.key);
          search.setAttribute("aria-label", `Search ${column.label} options`);
          search.placeholder = "Find an option…";
          details.append(search);
        }

        const optionList = document.createElement("div");
        optionList.className = "datagrid-filter-options";
        optionList.setAttribute("role", "group");
        optionList.setAttribute("aria-label", `${column.label} values`);
        for (const option of options) {
          const label = document.createElement("label");
          label.className = "datagrid-filter-option";
          label.setAttribute("data-dg-filter-value", option.value);
          label.setAttribute("data-dg-normalized-label", normalizeText(option.label));

          const checkbox = document.createElement("input");
          checkbox.type = "checkbox";
          checkbox.value = option.value;
          checkbox.setAttribute("data-dg-filter-column", column.key);

          const text = document.createElement("span");
          text.className = "datagrid-filter-option-label";
          text.textContent = option.label;

          const count = document.createElement("span");
          count.className = "datagrid-filter-count";
          count.setAttribute("data-dg-filter-count", "");
          count.setAttribute("aria-label", "0 rows");
          count.textContent = "0";
          label.append(checkbox, text, count);
          optionList.append(label);
        }
        details.append(optionList);
        wrapper.append(details);
      }

      this._filterHost.append(wrapper);
    }

    _hasUnknownSelectedOptions(state) {
      if (!this._filterHost) {
        return false;
      }
      for (const [column, values] of Object.entries(state.filters)) {
        const inputs = this._findAllOwned(
          `input[type="checkbox"][data-dg-filter-column="${this._escapeSelector(column)}"]`,
        );
        const known = new Set(inputs.map((input) => input.value));
        if (values.some((value) => !known.has(value))) {
          return true;
        }
      }
      return false;
    }

    _apply({ writeQuery }) {
      this._state = this._normalizeState(this._state);
      const hooks = this._resolveHooks();
      const hasHooks = hooks.search.length > 0 || hooks.filter.length > 0 || hooks.compare.length > 0;
      this._hookState = hasHooks ? cloneState(this._state) : null;
      const searchTokens = tokenize(this._state.search);

      const ordered = [...this._records];
      const sortColumn = this._columns.get(this._state.sort);
      if (sortColumn?.sortable) {
        ordered.sort((a, b) => this._compareRecords(a, b, sortColumn, hooks.compare));
      } else {
        ordered.sort((a, b) => a.index - b.index);
      }

      if (this._tbody && ordered.length > 0) {
        const current = [...this._tbody.children]
          .filter((row) => row.matches("tr[data-dg-row]") && this._owns(row));
        const changed = current.length !== ordered.length ||
          ordered.some((record, index) => current[index] !== record.element);
        if (changed && typeof this._tbody.moveBefore === "function") {
          for (const record of ordered) {
            this._tbody.moveBefore(record.element, null);
          }
        } else if (changed) {
          const fragment = document.createDocumentFragment();
          for (const record of ordered) {
            fragment.append(record.element);
          }
          this._tbody.append(fragment);
        }
      }

      const matching = ordered.filter(
        (record) =>
          this._matchesSearch(record, searchTokens, hooks.search) &&
          this._matchesFilters(record, "", hooks.filter),
      );
      const paginated = this._features.has("pagination") && this._pageSize > 0;
      const totalPages = paginated
        ? Math.max(1, Math.ceil(matching.length / this._pageSize))
        : 1;
      this._state.page = paginated
        ? Math.min(Math.max(1, this._state.page), totalPages)
        : 1;
      if (this._hookState) {
        this._hookState.page = this._state.page;
      }

      const first = paginated ? (this._state.page - 1) * this._pageSize : 0;
      const visibleRecords = paginated
        ? matching.slice(first, first + this._pageSize)
        : matching;
      const visible = new Set(visibleRecords.map((record) => record.element));
      for (const record of this._records) {
        record.element.hidden = !visible.has(record.element);
      }

      this._syncControls();
      this._syncFacetCounts(searchTokens, hooks);
      this._renderPagination(totalPages);
      this._renderSummary(matching.length, first, visibleRecords.length);
      if (this._empty) {
        this._empty.hidden = matching.length !== 0;
      }
      if (writeQuery && this._features.has("query")) {
        this._encodeQuery();
      }
    }

    _matchesSearch(record, tokens, hooks) {
      if (!this._features.has("search")) {
        return true;
      }
      const defaultResult = tokens.every((token) => record.searchText.includes(token));
      if (hooks.length === 0) {
        return defaultResult;
      }
      const result = this._callHook(hooks, {
        grid: this,
        row: record.element,
        state: this._hookState,
        query: this._state.search,
        tokens: [...tokens],
        text: record.searchText,
        values: Object.fromEntries(record.values),
        defaultResult,
      });
      return result === undefined ? defaultResult : Boolean(result);
    }

    _matchesFilters(record, excludedColumn, hooks) {
      if (!this._features.has("filters")) {
        return true;
      }
      for (const [column, selected] of Object.entries(this._state.filters)) {
        if (column === excludedColumn || selected.length === 0) {
          continue;
        }
        const values = (record.filterValues.get(column) || []).map((option) => option.value);
        const selectedSet = new Set(selected);
        const defaultResult = values.some((value) => selectedSet.has(value));
        if (hooks.length === 0) {
          if (!defaultResult) {
            return false;
          }
          continue;
        }
        const result = this._callHook(hooks, {
          grid: this,
          row: record.element,
          state: this._hookState,
          column,
          selected: [...selected],
          values: [...values],
          defaultResult,
        });
        if (!(result === undefined ? defaultResult : Boolean(result))) {
          return false;
        }
      }
      return true;
    }

    _compareRecords(a, b, column, hooks) {
      const aValue = a.sortValues.get(column.key) ?? "";
      const bValue = b.sortValues.get(column.key) ?? "";
      const defaultResult = this._builtInComparison(
        aValue,
        bValue,
        column.sortKind,
        this._state.descending,
      );
      const customResult = hooks.length === 0
        ? undefined
        : this._callHook(hooks, {
          grid: this,
          a: a.element,
          b: b.element,
          state: this._hookState,
          column: column.key,
          kind: column.sortKind,
          aValue,
          bValue,
          descending: this._state.descending,
          defaultResult,
        });
      let result = customResult === undefined ? defaultResult : Number(customResult);
      if (!Number.isFinite(result)) {
        result = defaultResult;
      }
      return result || a.index - b.index;
    }

    _builtInComparison(aRaw, bRaw, kind, descending) {
      const a = this._typedValue(aRaw, kind);
      const b = this._typedValue(bRaw, kind);
      if (a.missing !== b.missing) {
        return a.missing ? 1 : -1;
      }
      if (a.missing) {
        return 0;
      }

      let result;
      if (kind === "text" || kind === "natural" || kind === "") {
        result = naturalCollator.compare(a.value, b.value);
      } else {
        result = a.value < b.value ? -1 : a.value > b.value ? 1 : 0;
      }
      return descending ? -result : result;
    }

    _typedValue(raw, kind) {
      const text = String(raw ?? "").trim();
      if (text === "") {
        return { missing: true, value: null };
      }
      switch (kind) {
        case "number": {
          const value = Number(text);
          return Number.isFinite(value)
            ? { missing: false, value }
            : { missing: true, value: null };
        }
        case "time":
        case "date":
        case "datetime": {
          const value = Date.parse(text);
          return Number.isFinite(value)
            ? { missing: false, value }
            : { missing: true, value: null };
        }
        case "boolean":
        case "bool": {
          const value = text.toLowerCase();
          if (["true", "1", "yes", "on"].includes(value)) {
            return { missing: false, value: 1 };
          }
          if (["false", "0", "no", "off"].includes(value)) {
            return { missing: false, value: 0 };
          }
          return { missing: true, value: null };
        }
        default:
          return { missing: false, value: text };
      }
    }

    _resolveHooks() {
      const result = { search: [], filter: [], compare: [] };
      for (const hookName of this._hookNames) {
        const registered = hookRegistry.get(hookName);
        for (const name of ["search", "filter", "compare"]) {
          if (typeof registered?.[name] === "function") {
            result[name].push(registered[name]);
          }
        }
      }
      return result;
    }

    _callHook(hooks, context) {
      for (const hook of hooks) {
        const value = hook(context);
        if (value !== undefined) {
          return value;
        }
      }
      return undefined;
    }

    _syncControls() {
      if (this._searchInput) {
        const enabled = this._features.has("search");
        // A search box over a scannable table is noise for the same
        // reason an auto facet is (see _renderFilters). Active search
        // text still renders so it stays visible and removable, and a
        // focused box is never yanked out from under the user clearing
        // that text — it hides on the next state change after blur.
        const useless =
          this._records.length <= scannableRowCount &&
          this._state.search === "" &&
          document.activeElement !== this._searchInput;
        this._searchInput.disabled = !enabled || useless;
        this._searchInput.hidden = !enabled || useless;
        const wrap = this._searchInput.closest(".datagrid-search-wrap");
        if (wrap) {
          wrap.hidden = !enabled || useless;
        }
        if (this._searchInput.value !== this._state.search) {
          this._searchInput.value = this._state.search;
        }
      }

      for (const column of this._columns.values()) {
        if (column.header.getAttribute("aria-sort")) {
          column.header.removeAttribute("aria-sort");
        }
        if (column.sortButton) {
          column.sortButton.disabled = !column.sortable;
          if (column.sortable) {
            const nextDirection = this._state.sort !== column.key
              ? "ascending"
              : this._state.descending
                ? "none"
                : "descending";
            column.sortButton.setAttribute(
              "aria-label",
              nextDirection === "none"
                ? `${column.label}: clear sorting`
                : `${column.label}: sort ${nextDirection}`,
            );
          }
        }
      }
      const sortedColumn = this._columns.get(this._state.sort);
      if (sortedColumn?.sortable) {
        sortedColumn.header.setAttribute(
          "aria-sort",
          this._state.descending ? "descending" : "ascending",
        );
      }

      for (const checkbox of this._findAllOwned(
        '.datagrid-generated-filters input[type="checkbox"][data-dg-filter-column]',
      )) {
        const column = checkbox.getAttribute("data-dg-filter-column") || "";
        checkbox.checked = (this._state.filters[column] || []).includes(checkbox.value);
      }

      const active =
        this._state.search !== "" ||
        this._state.sort !== "" ||
        Object.keys(this._state.filters).length > 0;
      this.setAttribute("data-dg-has-active-state", String(active));
      for (const clear of this._findAllOwned('[data-dg-role="clear"]')) {
        clear.disabled = !active;
        clear.hidden = !active;
      }
    }

    _syncFacetCounts(searchTokens, hooks) {
      for (const details of this._findAllOwned("details[data-dg-filter-column]")) {
        const column = details.getAttribute("data-dg-filter-column") || "";
        const counts = new Map();
        for (const record of this._records) {
          if (
            !this._matchesSearch(record, searchTokens, hooks.search) ||
            !this._matchesFilters(record, column, hooks.filter)
          ) {
            continue;
          }
          const values = new Set(
            (record.filterValues.get(column) || []).map((option) => option.value),
          );
          for (const value of values) {
            counts.set(value, (counts.get(value) || 0) + 1);
          }
        }

        const checkedLabels = [];
        for (const option of details.querySelectorAll("label[data-dg-filter-value]")) {
          const value = option.getAttribute("data-dg-filter-value") || "";
          const checkbox = option.querySelector('input[type="checkbox"]');
          const count = counts.get(value) || 0;
          const countNode = option.querySelector("[data-dg-filter-count]");
          if (countNode) {
            countNode.textContent = String(count);
            countNode.setAttribute("aria-label", `${count} ${count === 1 ? "row" : "rows"}`);
          }
          if (checkbox) {
            checkbox.disabled = count === 0 && !checkbox.checked;
            if (checkbox.checked) {
              checkedLabels.push(
                option.querySelector(".datagrid-filter-option-label")?.textContent || value,
              );
            }
          }
        }

        const selection = details.querySelector("[data-dg-filter-selection]");
        if (selection) {
          selection.textContent = checkedLabels.length === 0
            ? "All"
            : checkedLabels.length === 1
              ? checkedLabels[0]
              : `${checkedLabels.length} selected`;
        }
        details.classList.toggle("is-active", checkedLabels.length > 0);
      }
    }

    _renderPagination(totalPages) {
      if (!this._pagination) {
        return;
      }
      const active = document.activeElement;
      const focusedPage = this._pagination.contains(active)
        ? active?.getAttribute?.("data-dg-page") || ""
        : "";
      const focusedAction = this._pagination.contains(active)
        ? active?.getAttribute?.("data-dg-page-action") || ""
        : "";
      this._pagination.replaceChildren();
      const enabled = this._features.has("pagination") && this._pageSize > 0;
      this._pagination.hidden = !enabled || totalPages <= 1;
      if (!enabled) {
        return;
      }

      this._pagination.setAttribute("aria-label", "Table pages");
      this._pagination.append(
        this._pageActionButton("first", "First page", "«", this._state.page === 1),
        this._pageActionButton("previous", "Previous page", "‹", this._state.page === 1),
      );

      for (const item of pageItems(this._state.page, totalPages)) {
        if (item === "ellipsis") {
          const ellipsis = document.createElement("span");
          ellipsis.className = "datagrid-page-ellipsis";
          ellipsis.setAttribute("aria-hidden", "true");
          ellipsis.textContent = "…";
          this._pagination.append(ellipsis);
          continue;
        }
        const button = document.createElement("button");
        button.type = "button";
        button.className = "datagrid-page";
        button.setAttribute("data-dg-page", String(item));
        button.setAttribute("aria-label", `Page ${item}`);
        button.textContent = String(item);
        if (item === this._state.page) {
          button.classList.add("is-current");
          button.setAttribute("aria-current", "page");
          button.disabled = true;
        }
        this._pagination.append(button);
      }

      this._pagination.append(
        this._pageActionButton(
          "next",
          "Next page",
          "›",
          this._state.page === totalPages,
        ),
        this._pageActionButton(
          "last",
          "Last page",
          "»",
          this._state.page === totalPages,
        ),
      );

      let replacement = null;
      if (focusedAction !== "") {
        replacement = this._pagination.querySelector(
          `[data-dg-page-action="${this._escapeSelector(focusedAction)}"]`,
        );
      } else if (focusedPage !== "") {
        replacement = this._pagination.querySelector(
          `[data-dg-page="${this._escapeSelector(focusedPage)}"]`,
        );
      }
      if (replacement && !replacement.disabled) {
        replacement.focus({ preventScroll: true });
      } else if (focusedAction !== "" || focusedPage !== "") {
        this._pagination.tabIndex = -1;
        this._pagination.focus({ preventScroll: true });
      }
    }

    _pageActionButton(action, label, text, disabled) {
      const button = document.createElement("button");
      button.type = "button";
      button.className = `datagrid-page datagrid-page-${action}`;
      button.setAttribute("data-dg-page-action", action);
      button.setAttribute("aria-label", label);
      button.textContent = text;
      button.disabled = disabled;
      return button;
    }

    _renderSummary(matching, first, visibleCount) {
      if (!this._summary) {
        return;
      }
      const total = this._records.length;
      if (matching === 0) {
        this._summary.textContent = total === 0 ? "No rows" : `No matching rows · ${total} total`;
        return;
      }
      const paginated = this._features.has("pagination") && this._pageSize > 0;
      const range = paginated
        ? `Showing ${first + 1}–${first + visibleCount} of ${matching}`
        : `${matching}`;
      const noun = matching === 1 ? "row" : "rows";
      this._summary.textContent = matching === total
        ? `${range} ${noun}`
        : `${range} matching ${noun} · ${total} total`;
    }

    _decodeQuery() {
      const defaults = cloneState(this._defaults);
      const parameters = new URL(window.location.href).searchParams;
      const key = (suffix) => `${this._queryPrefix}.${suffix}`;
      const stateSuffixes = ["state"];
      if (this._features.has("search")) {
        stateSuffixes.push("search");
      }
      if (this._features.has("sort")) {
        stateSuffixes.push("sort", "dir");
      }
      if (this._features.has("pagination")) {
        stateSuffixes.push("page");
      }
      let hasState = stateSuffixes
        .some((suffix) => parameters.has(key(suffix)));
      if (!hasState) {
        const filterPrefix = key("filter.");
        for (const parameterKey of parameters.keys()) {
          if (
            parameterKey.startsWith(filterPrefix) &&
            this._columns.get(parameterKey.slice(filterPrefix.length))?.filterable
          ) {
            hasState = true;
            break;
          }
        }
      }
      if (!hasState) {
        return defaults;
      }

      const state = {
        search: parameters.get(key("search")) || "",
        filters: {},
        sort: parameters.get(key("sort")) || "",
        descending: parameters.get(key("dir")) === "desc",
        page: 1,
      };
      const page = queryInteger(parameters.get(key("page")));
      if (page !== null) {
        state.page = page;
      }
      const filterPrefix = key("filter.");
      for (const parameterKey of new Set(parameters.keys())) {
        if (!parameterKey.startsWith(filterPrefix)) {
          continue;
        }
        const column = parameterKey.slice(filterPrefix.length);
        if (column !== "" && this._columns.get(column)?.filterable) {
          state.filters[column] = parameters.getAll(parameterKey);
        }
      }
      return this._normalizeState(state);
    }

    _encodeQuery() {
      const url = new URL(window.location.href);
      const namespace = `${this._queryPrefix}.`;
      const ownedKeys = new Set(
        [...url.searchParams.keys()].filter((key) => key.startsWith(namespace)),
      );
      for (const key of ownedKeys) {
        url.searchParams.delete(key);
      }

      const state = this._normalizeState(this._state);
      const defaults = this._normalizeState(this._defaults);
      if (!statesEqual(state, defaults)) {
        const key = (suffix) => `${this._queryPrefix}.${suffix}`;
        url.searchParams.set(key("state"), "1");
        if (this._features.has("search") && state.search !== "") {
          url.searchParams.set(key("search"), state.search);
        }
        if (this._features.has("sort") && state.sort !== "") {
          url.searchParams.set(key("sort"), state.sort);
          url.searchParams.set(key("dir"), state.descending ? "desc" : "asc");
        }
        if (this._features.has("pagination") && state.page > 1) {
          url.searchParams.set(key("page"), String(state.page));
        }
        for (const column of Object.keys(state.filters).sort(compareCanonicalStrings)) {
          for (const value of state.filters[column]) {
            url.searchParams.append(key(`filter.${column}`), value);
          }
        }
      }

      const next = `${url.pathname}${url.search}${url.hash}`;
      const current = `${window.location.pathname}${window.location.search}${window.location.hash}`;
      if (next !== current) {
        window.history.replaceState(window.history.state, "", next);
      }
    }

    _setState(next, { emit, writeQuery, renderUnknowns }) {
      const previous = this.getState();
      this._state = this._normalizeState(next);
      if (renderUnknowns && this._hasUnknownSelectedOptions(this._state)) {
        this._renderFilters();
      }
      this._apply({ writeQuery });
      if (emit && !statesEqual(previous, this._state)) {
        this.dispatchEvent(new CustomEvent("datagrid:change", {
          bubbles: true,
          composed: true,
          detail: this.getState(),
        }));
      }
    }

    _bindEvents() {
      this._abortController?.abort();
      this._abortController = new AbortController();
      const options = { signal: this._abortController.signal };
      this.addEventListener("click", (event) => this._onClick(event), options);
      this.addEventListener("input", (event) => this._onInput(event), options);
      this.addEventListener("change", (event) => this._onChange(event), options);
      this.addEventListener("keydown", (event) => this._onKeyDown(event), options);
      window.addEventListener("popstate", () => this._onPopState(), options);
      // An open facet is a menu painted over the page, so a press anywhere
      // else dismisses it — including in another grid, which listens on the
      // same document and closes only its own facets.
      (this.ownerDocument || document).addEventListener(
        "pointerdown",
        (event) => this._onDocumentPointerDown(event),
        options,
      );
    }

    _onDocumentPointerDown(event) {
      const target = event.target instanceof Element ? event.target : null;
      this._closeFacets(target?.closest("details[data-dg-filter-column]") || null);
    }

    _onKeyDown(event) {
      if (event.key !== "Escape" || event.defaultPrevented) {
        return;
      }
      const target = event.target instanceof Element ? event.target : null;
      const details = target?.closest("details[data-dg-filter-column]");
      if (!details || !details.open || !this._owns(details)) {
        return;
      }
      details.open = false;
      details.querySelector("summary")?.focus();
    }

    // Close every open facet but one. The selector matches only open facets,
    // so the common press — nothing open — walks an empty list.
    _closeFacets(except) {
      for (const details of this._findAllOwned("details[data-dg-filter-column][open]")) {
        if (details !== except) {
          details.open = false;
        }
      }
    }

    _onClick(event) {
      const target = event.target instanceof Element ? event.target : null;
      if (!target || !this._owns(target)) {
        return;
      }

      const facetSummary = target.closest("details[data-dg-filter-column] > summary");
      if (facetSummary) {
        // Keyboard activation raises no pointer event, so the press that
        // opens a facet from the keyboard closes the others here instead.
        this._closeFacets(facetSummary.parentElement);
        return;
      }

      const sortButton = target.closest('[data-dg-role="sort"]');
      if (sortButton && this._owns(sortButton)) {
        const header = sortButton.closest("th[data-dg-column]");
        const column = header?.getAttribute("data-dg-column") || "";
        if (!this._columns.get(column)?.sortable) {
          return;
        }
        const next = this.getState();
        next.page = 1;
        if (next.sort !== column) {
          next.sort = column;
          next.descending = false;
        } else if (!next.descending) {
          next.descending = true;
        } else {
          next.sort = "";
          next.descending = false;
        }
        this._setState(next, { emit: true, writeQuery: true, renderUnknowns: false });
        return;
      }

      const clear = target.closest('[data-dg-role="clear"]');
      if (clear && this._owns(clear)) {
        this._setState({
          search: "",
          filters: {},
          sort: "",
          descending: false,
          page: 1,
        }, { emit: true, writeQuery: true, renderUnknowns: false });
        return;
      }

      const pageButton = target.closest("[data-dg-page], [data-dg-page-action]");
      if (!pageButton || !this._owns(pageButton) || pageButton.disabled) {
        return;
      }
      const next = this.getState();
      const explicitPage = positiveInteger(pageButton.getAttribute("data-dg-page"));
      if (explicitPage) {
        next.page = explicitPage;
      } else {
        const hooks = this._resolveHooks();
        const hasHooks = hooks.search.length > 0 || hooks.filter.length > 0 || hooks.compare.length > 0;
        this._hookState = hasHooks ? cloneState(this._state) : null;
        const searchTokens = tokenize(this._state.search);
        const totalPages = Math.max(
          1,
          Math.ceil(
            this._records.filter(
              (record) =>
                this._matchesSearch(record, searchTokens, hooks.search) &&
                this._matchesFilters(record, "", hooks.filter),
            ).length / this._pageSize,
          ),
        );
        switch (pageButton.getAttribute("data-dg-page-action")) {
          case "first":
            next.page = 1;
            break;
          case "previous":
            next.page -= 1;
            break;
          case "next":
            next.page += 1;
            break;
          case "last":
            next.page = totalPages;
            break;
          default:
            return;
        }
      }
      this._setState(next, { emit: true, writeQuery: true, renderUnknowns: false });
    }

    _onInput(event) {
      const target = event.target;
      if (!(target instanceof HTMLInputElement) || !this._owns(target)) {
        return;
      }
      if (target.matches('[data-dg-role="filter-search"]')) {
        this._filterLocalOptions(target);
        return;
      }
      if (target.matches('[data-dg-role="search"]') && this._features.has("search")) {
        const next = this.getState();
        next.search = target.value;
        next.page = 1;
        this._setState(next, { emit: true, writeQuery: true, renderUnknowns: false });
      }
    }

    _onChange(event) {
      const target = event.target;
      if (!(target instanceof HTMLInputElement) || !this._owns(target)) {
        return;
      }
      if (target.matches('input[type="checkbox"][data-dg-filter-column]')) {
        const column = target.getAttribute("data-dg-filter-column") || "";
        if (!this._columns.get(column)?.filterable) {
          return;
        }
        const next = this.getState();
        const selected = new Set(next.filters[column] || []);
        if (target.checked) {
          selected.add(target.value);
        } else {
          selected.delete(target.value);
        }
        const values = uniqueSortedStrings([...selected]);
        if (values.length > 0) {
          next.filters[column] = values;
        } else {
          delete next.filters[column];
        }
        next.page = 1;
        this._setState(next, { emit: true, writeQuery: true, renderUnknowns: false });
      }
    }

    _filterLocalOptions(input) {
      const details = input.closest("details[data-dg-filter-column]");
      if (!details) {
        return;
      }
      const tokens = tokenize(input.value);
      for (const option of details.querySelectorAll("label[data-dg-filter-value]")) {
        const text = option.getAttribute("data-dg-normalized-label") || "";
        const checkbox = option.querySelector('input[type="checkbox"]');
        option.hidden = !checkbox?.checked && !tokens.every((token) => text.includes(token));
      }
    }

    _onPopState() {
      if (!this._connected || !this._features.has("query")) {
        return;
      }
      const next = this._decodeQuery();
      this._setState(next, { emit: true, writeQuery: true, renderUnknowns: true });
    }

    _findOwned(selector) {
      return this._findAllOwned(selector)[0] || null;
    }

    _findAllOwned(selector) {
      return [...this.querySelectorAll(selector)].filter((node) => this._owns(node));
    }

    _owns(node) {
      return node.closest(tagName) === this;
    }

    _escapeSelector(value) {
      if (window.CSS?.escape) {
        return window.CSS.escape(value);
      }
      return String(value).replace(/["\\]/g, "\\$&");
    }
  }

  // Guarded because this asset can be emitted more than once on a page:
  // a host embedding someone else's rendered body — backupd's dashboard
  // inside opsui chrome is the case that found this — may already have
  // loaded it, and a second define() throws NotSupportedError and takes
  // the rest of this script down with it. First definition wins; they
  // are the same class.
  if (!customElements.get("monks-datagrid")) {
    customElements.define("monks-datagrid", DatagridElement);
  }
})();
