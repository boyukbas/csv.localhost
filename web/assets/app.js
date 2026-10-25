/* csv.localhost viewer behaviour --------------------------------------- */
(function () {
  "use strict";

  var root = document.documentElement;
  var THEME_KEY = "csvl.theme";

  /* ---- theme (shared with the listing pages) ------------------------- */

  function storedTheme() {
    try { return localStorage.getItem(THEME_KEY) || "auto"; } catch (e) { return "auto"; }
  }
  function storeTheme(v) {
    try { localStorage.setItem(THEME_KEY, v); } catch (e) { /* private mode */ }
  }
  function prefersDark() {
    return window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches;
  }
  function applyTheme() {
    var t = storedTheme();
    root.classList.toggle("theme-dark", t === "dark" || (t === "auto" && prefersDark()));
    root.setAttribute("data-theme", t);
  }
  applyTheme();

  if (window.matchMedia) {
    var mq = window.matchMedia("(prefers-color-scheme: dark)");
    var onScheme = function () { if (storedTheme() === "auto") { applyTheme(); } };
    if (mq.addEventListener) { mq.addEventListener("change", onScheme); }
    else if (mq.addListener) { mq.addListener(onScheme); }
  }

  var themeBtn = document.getElementById("theme-toggle");
  if (themeBtn) {
    themeBtn.title = "Theme: " + storedTheme();
    themeBtn.addEventListener("click", function () {
      var order = ["auto", "light", "dark"];
      var next = order[(order.indexOf(storedTheme()) + 1) % order.length];
      storeTheme(next);
      themeBtn.title = "Theme: " + next;
      applyTheme();
    });
  }

  var boot = window.__CSVL;
  if (!boot) { return; } // a listing page: theme is all it needs

  /* ---- elements ------------------------------------------------------ */

  var grid = document.getElementById("grid");
  var table = document.getElementById("table");
  var colgroup = document.getElementById("cols");
  var thead = document.getElementById("head");
  var tbody = document.getElementById("body");
  var notices = document.getElementById("notices");
  var counts = document.getElementById("counts");
  var emptyState = document.getElementById("empty");
  var search = document.getElementById("search");
  var searchClear = document.getElementById("search-clear");
  var popover = document.getElementById("popover");
  var sheet = document.getElementById("options");
  var inspector = document.getElementById("inspector");
  var insLabel = document.getElementById("ins-label");
  var insValue = document.getElementById("ins-value");

  var ROW_H = parseInt(getComputedStyle(root).getPropertyValue("--row-h"), 10) || 27;
  var CHUNK = 300;   // rows fetched per request
  var OVER = 8;      // rows rendered beyond the viewport
  var PRINT_MAX = 5000;

  var state = {
    columns: boot.columns || [],
    total: boot.total || 0,
    sort: -1,
    dir: "asc",
    q: "",
    chunks: new Map(),
    pending: new Set(),
    seed: null,
    selected: null,
    selectedValue: "",
    freeze: false,
    widths: {}
  };

  /* ---- helpers ------------------------------------------------------- */

  function params(extra) {
    var p = new URLSearchParams();
    p.set("p", boot.urlPath);
    var o = boot.options || {};
    if (o.delimiter) { p.set("d", o.delimiter); }
    if (o.header && o.header !== "auto") { p.set("header", o.header); }
    if (o.encoding) { p.set("enc", o.encoding); }
    for (var k in extra) {
      if (extra[k] !== undefined && extra[k] !== null && extra[k] !== "") { p.set(k, extra[k]); }
    }
    return p;
  }

  function api(path, extra) {
    return fetch(path + "?" + params(extra).toString(), {
      credentials: "same-origin",
      cache: "no-store"
    }).then(function (r) {
      if (!r.ok) { return r.text().then(function (t) { throw new Error(t || ("status " + r.status)); }); }
      return r.json();
    });
  }

  function isNumericKind(kind) { return kind === "int" || kind === "number"; }

  function kindTag(kind) {
    switch (kind) {
      case "int": case "number": return "#";
      case "date": return "D";
      case "bool": return "✓";
      default: return "A";
    }
  }
  function kindLabel(kind) {
    switch (kind) {
      case "int": return "whole numbers";
      case "number": return "numbers";
      case "date": return "dates";
      case "bool": return "yes / no";
      default: return "text";
    }
  }

  function group(n) {
    return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, " ");
  }

  function widthKey() { return "csvl.w." + boot.path; }

  function loadWidths() {
    try {
      var raw = localStorage.getItem(widthKey());
      if (raw) { state.widths = JSON.parse(raw) || {}; }
    } catch (e) { state.widths = {}; }
  }
  function saveWidths() {
    try { localStorage.setItem(widthKey(), JSON.stringify(state.widths)); } catch (e) { /* ignore */ }
  }

  /* ---- notices ------------------------------------------------------- */

  function showNotices() {
    notices.innerHTML = "";
    var list = [];
    if (boot.error) { list.push({ text: boot.error, error: true }); }
    if (boot.truncated) {
      list.push({ text: "This file is larger than the viewer keeps in memory - only the first rows are shown." });
    }
    (boot.warnings || []).forEach(function (wtext) { list.push({ text: wtext }); });

    list.forEach(function (item) {
      var d = document.createElement("div");
      d.className = "notice" + (item.error ? " error" : "");
      d.textContent = item.text;
      notices.appendChild(d);
    });
  }

  /* ---- header -------------------------------------------------------- */

  function buildHeader() {
    colgroup.innerHTML = "";
    thead.innerHTML = "";

    var rownumCol = document.createElement("col");
    rownumCol.style.width = getComputedStyle(root).getPropertyValue("--rownum-w").trim() || "62px";
    colgroup.appendChild(rownumCol);

    var tr = document.createElement("tr");
    var corner = document.createElement("th");
    corner.className = "rownum";
    corner.title = "Row number in the file";
    corner.textContent = "#";
    tr.appendChild(corner);

    state.columns.forEach(function (col, i) {
      var c = document.createElement("col");
      c.style.width = (state.widths[col.name] || col.width || 140) + "px";
      colgroup.appendChild(c);

      var th = document.createElement("th");
      th.dataset.col = i;
      if (i === 0) { th.className = "frozen"; }
      if (state.sort === i) { th.classList.add("sorted"); }
      th.title = col.name + " - " + kindLabel(col.kind);

      var inner = document.createElement("div");
      inner.className = "th-inner";

      var kind = document.createElement("span");
      kind.className = "th-kind";
      kind.textContent = kindTag(col.kind);
      inner.appendChild(kind);

      var name = document.createElement("span");
      name.className = "th-name";
      name.textContent = col.name;
      inner.appendChild(name);

      if (state.sort === i) {
        var arrow = document.createElement("span");
        arrow.className = "th-arrow";
        arrow.textContent = state.dir === "desc" ? "▼" : "▲";
        inner.appendChild(arrow);
      }

      var menu = document.createElement("button");
      menu.type = "button";
      menu.className = "th-menu";
      menu.title = "Column summary";
      menu.textContent = "▾";
      menu.addEventListener("click", function (ev) {
        ev.stopPropagation();
        openStats(i, th);
      });
      inner.appendChild(menu);

      th.appendChild(inner);

      var res = document.createElement("div");
      res.className = "resizer";
      res.addEventListener("mousedown", function (ev) { startResize(ev, i); });
      res.addEventListener("click", function (ev) { ev.stopPropagation(); });
      res.addEventListener("dblclick", function (ev) {
        ev.stopPropagation();
        delete state.widths[col.name];
        saveWidths();
        buildHeader();
        render();
      });
      th.appendChild(res);

      th.addEventListener("click", function () { cycleSort(i); });
      th.addEventListener("contextmenu", function (ev) {
        ev.preventDefault();
        openStats(i, th);
      });
      tr.appendChild(th);
    });

    thead.appendChild(tr);
    sizeTable();
  }

  function sizeTable() {
    var total = 0;
    for (var i = 0; i < colgroup.children.length; i++) {
      total += parseInt(colgroup.children[i].style.width, 10) || 0;
    }
    table.style.width = total > grid.clientWidth ? total + "px" : "100%";
  }

  var resizing = null;

  function startResize(ev, index) {
    ev.preventDefault();
    ev.stopPropagation();
    var col = colgroup.children[index + 1];
    resizing = {
      index: index,
      startX: ev.clientX,
      startW: parseInt(col.style.width, 10) || 140
    };
    document.body.style.cursor = "col-resize";
  }

  document.addEventListener("mousemove", function (ev) {
    if (!resizing) { return; }
    var w = Math.max(48, resizing.startW + (ev.clientX - resizing.startX));
    colgroup.children[resizing.index + 1].style.width = w + "px";
    sizeTable();
  });

  document.addEventListener("mouseup", function () {
    if (!resizing) { return; }
    var col = state.columns[resizing.index];
    state.widths[col.name] = parseInt(colgroup.children[resizing.index + 1].style.width, 10);
    saveWidths();
    resizing = null;
    document.body.style.cursor = "";
  });

  /* ---- data ---------------------------------------------------------- */

  function chunkOf(row) { return Math.floor(row / CHUNK); }

  function rowAt(i) {
    var c = state.chunks.get(chunkOf(i));
    if (c) {
      var k = i - c.offset;
      if (k >= 0 && k < c.rows.length) { return { cells: c.rows[k], id: c.ids[k] }; }
    }
    // The page arrived with enough rows inlined to paint the first screen
    // before any request goes out; they stand in until the chunk lands.
    var seed = state.seed;
    if (seed && i < seed.rows.length) { return { cells: seed.rows[i], id: seed.ids[i] }; }
    return null;
  }

  function fetchChunk(n) {
    if (state.chunks.has(n) || state.pending.has(n)) { return; }
    state.pending.add(n);
    api("/api/rows", {
      offset: n * CHUNK,
      limit: CHUNK,
      sort: state.sort >= 0 ? state.sort : "",
      dir: state.dir,
      q: state.q
    }).then(function (page) {
      state.pending.delete(n);
      state.chunks.set(n, page);
      if (page.total !== state.total) {
        state.total = page.total;
        updateCounts();
      }
      // Keep memory bounded on a long scroll through a big file.
      if (state.chunks.size > 40) {
        var oldest = state.chunks.keys().next().value;
        state.chunks.delete(oldest);
      }
      render();
    }).catch(function (err) {
      state.pending.delete(n);
      console.error("csv.localhost:", err);
    });
  }

  function reload(keepScroll) {
    var top = keepScroll ? grid.scrollTop : 0;
    state.chunks.clear();
    state.pending.clear();
    state.seed = null;
    api("/api/rows", {
      offset: 0,
      limit: CHUNK,
      sort: state.sort >= 0 ? state.sort : "",
      dir: state.dir,
      q: state.q
    }).then(function (page) {
      state.chunks.set(0, page);
      state.total = page.total;
      updateCounts();
      render();
      grid.scrollTop = Math.min(top, Math.max(0, state.total * ROW_H - grid.clientHeight));
      render();
    }).catch(function (err) {
      console.error("csv.localhost:", err);
    });
  }

  /* ---- rendering ----------------------------------------------------- */

  var renderQueued = false;

  function queueRender() {
    if (renderQueued) { return; }
    renderQueued = true;
    requestAnimationFrame(function () {
      renderQueued = false;
      render();
    });
  }

  function render() {
    var total = state.total;
    emptyState.hidden = total !== 0;
    if (total === 0) {
      emptyState.textContent = state.q
        ? "No rows match " + JSON.stringify(state.q) + "."
        : "This file has no data rows.";
      tbody.innerHTML = "";
      return;
    }

    var viewH = grid.clientHeight || 600;
    var first = Math.max(0, Math.floor(grid.scrollTop / ROW_H) - OVER);
    var visible = Math.ceil(viewH / ROW_H) + OVER * 2;
    var last = Math.min(total, first + visible);

    for (var n = chunkOf(first); n <= chunkOf(Math.max(first, last - 1)); n++) {
      fetchChunk(n);
    }

    var frag = document.createDocumentFragment();
    frag.appendChild(spacer(first * ROW_H));

    for (var i = first; i < last; i++) {
      frag.appendChild(renderRow(i));
    }

    frag.appendChild(spacer((total - last) * ROW_H));
    tbody.replaceChildren(frag);
  }

  function spacer(height) {
    var tr = document.createElement("tr");
    tr.className = "spacer";
    var td = document.createElement("td");
    td.colSpan = state.columns.length + 1;
    td.style.height = Math.max(0, height) + "px";
    tr.appendChild(td);
    return tr;
  }

  function renderRow(i) {
    var data = rowAt(i);
    var tr = document.createElement("tr");
    tr.dataset.row = i;

    var num = document.createElement("td");
    num.className = "rownum";
    num.textContent = data ? data.id : "";
    tr.appendChild(num);

    state.columns.forEach(function (col, c) {
      var td = document.createElement("td");
      var cls = [];
      if (c === 0) { cls.push("frozen"); }
      if (isNumericKind(col.kind)) { cls.push("num"); }
      else if (col.kind === "bool") { cls.push("bool"); }
      var v = data ? (data.cells[c] || "") : "";
      if (data && v === "") { cls.push("blank"); }
      if (cls.length) { td.className = cls.join(" "); }
      td.textContent = v;
      if (v) { td.title = v; }
      if (state.selected && state.selected.row === i && state.selected.col === c) {
        td.classList.add("selected");
      }
      tr.appendChild(td);
    });
    return tr;
  }

  tbody.addEventListener("click", function (ev) {
    var td = ev.target.closest ? ev.target.closest("td") : null;
    if (!td || td.parentElement.classList.contains("spacer")) { return; }
    var row = parseInt(td.parentElement.dataset.row, 10);
    var col = td.cellIndex - 1;
    if (isNaN(row) || col < 0) { return; }
    selectCell(row, col);
  });

  function updateCounts() {
    var cols = state.columns.length;
    var text = group(state.total) + (state.total === 1 ? " row" : " rows");
    if (state.q && boot.total !== undefined && state.total !== boot.total) {
      text = group(state.total) + " of " + group(boot.total) + " rows";
    }
    counts.textContent = text + "  ·  " + cols + (cols === 1 ? " column" : " columns");
  }

  /* ---- sorting and filtering ----------------------------------------- */

  function cycleSort(i) {
    if (state.sort !== i) {
      state.sort = i;
      state.dir = "asc";
    } else if (state.dir === "asc") {
      state.dir = "desc";
    } else {
      state.sort = -1;
      state.dir = "asc";
    }
    state.selected = null;
    hideInspector();
    buildHeader();
    reload(false);
  }

  function setSort(i, dir) {
    state.sort = i;
    state.dir = dir;
    buildHeader();
    reload(false);
  }

  var searchTimer = null;

  search.addEventListener("input", function () {
    searchClear.hidden = search.value === "";
    clearTimeout(searchTimer);
    searchTimer = setTimeout(function () {
      state.q = search.value.trim();
      state.selected = null;
      hideInspector();
      reload(false);
    }, 200);
  });

  search.addEventListener("keydown", function (ev) {
    if (ev.key === "Escape") {
      search.value = "";
      search.dispatchEvent(new Event("input"));
      grid.focus();
    }
  });

  searchClear.addEventListener("click", function () {
    search.value = "";
    search.dispatchEvent(new Event("input"));
    search.focus();
  });

  /* ---- selection and the inspector ----------------------------------- */

  function selectCell(row, col) {
    state.selected = { row: row, col: col };
    render();
    var data = rowAt(row);
    if (!data) { return; }
    state.selectedValue = data.cells[col] || "";
    showInspector(state.columns[col].name + " · row " + data.id, state.selectedValue);
    api("/api/cell", { r: data.id, c: col }).then(function (res) {
      if (!state.selected || state.selected.row !== row || state.selected.col !== col) { return; }
      state.selectedValue = res.value;
      showInspector(res.column + " · row " + res.row, res.value);
    }).catch(function () { /* the clipped value is already shown */ });
  }

  function showInspector(label, value) {
    insLabel.textContent = label;
    insValue.textContent = value === "" ? "(empty)" : value;
    inspector.hidden = false;
  }

  function hideInspector() {
    inspector.hidden = true;
    insValue.textContent = "";
    state.selectedValue = "";
  }

  document.getElementById("ins-close").addEventListener("click", function () {
    state.selected = null;
    hideInspector();
    render();
  });

  document.getElementById("ins-copy").addEventListener("click", function () {
    copyText(state.selectedValue, this);
  });

  function copyText(text, btn) {
    var done = function () {
      if (!btn) { return; }
      var old = btn.textContent;
      btn.textContent = "Copied";
      setTimeout(function () { btn.textContent = old; }, 1300);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, function () { fallbackCopy(text, done); });
    } else {
      fallbackCopy(text, done);
    }
  }

  function fallbackCopy(text, done) {
    var ta = document.createElement("textarea");
    ta.value = text;
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand("copy"); done(); } catch (e) { /* ignore */ }
    document.body.removeChild(ta);
  }

  /* ---- keyboard ------------------------------------------------------ */

  document.addEventListener("keydown", function (ev) {
    if (ev.target === search || !ev.key) { return; }
    if (ev.key === "/" && !ev.ctrlKey && !ev.metaKey) {
      ev.preventDefault();
      search.focus();
      search.select();
      return;
    }
    if ((ev.ctrlKey || ev.metaKey) && ev.key === "c" && state.selected) {
      copyText(state.selectedValue, null);
      return;
    }
    if (ev.key === "Escape") {
      closePopover();
      closeSheet();
      return;
    }
    if (!state.selected) { return; }

    var r = state.selected.row, c = state.selected.col, moved = true;
    switch (ev.key) {
      case "ArrowDown": r = Math.min(state.total - 1, r + 1); break;
      case "ArrowUp": r = Math.max(0, r - 1); break;
      case "ArrowRight": c = Math.min(state.columns.length - 1, c + 1); break;
      case "ArrowLeft": c = Math.max(0, c - 1); break;
      case "PageDown": r = Math.min(state.total - 1, r + Math.floor(grid.clientHeight / ROW_H)); break;
      case "PageUp": r = Math.max(0, r - Math.floor(grid.clientHeight / ROW_H)); break;
      case "Home": r = 0; break;
      case "End": r = state.total - 1; break;
      default: moved = false;
    }
    if (!moved) { return; }
    ev.preventDefault();
    scrollRowIntoView(r);
    selectCell(r, c);
  });

  function scrollRowIntoView(r) {
    var top = r * ROW_H;
    var bottom = top + ROW_H;
    if (top < grid.scrollTop + ROW_H) {
      grid.scrollTop = Math.max(0, top - ROW_H);
    } else if (bottom > grid.scrollTop + grid.clientHeight) {
      grid.scrollTop = bottom - grid.clientHeight + 2;
    }
  }

  /* ---- column statistics --------------------------------------------- */

  var popoverFor = -1;

  function closePopover() {
    popover.hidden = true;
    popoverFor = -1;
    Array.prototype.forEach.call(document.querySelectorAll(".th-menu.open"), function (b) {
      b.classList.remove("open");
    });
  }

  function openStats(index, anchor) {
    if (popoverFor === index && !popover.hidden) { closePopover(); return; }
    closeSheet();
    popoverFor = index;

    var col = state.columns[index];
    popover.innerHTML = "";
    popover.hidden = false;
    positionNear(popover, anchor);

    var title = document.createElement("div");
    title.className = "pop-title";
    title.textContent = col.name;
    popover.appendChild(title);

    var kind = document.createElement("div");
    kind.className = "pop-kind";
    kind.textContent = kindLabel(col.kind) + (state.q ? " · filtered rows only" : "");
    popover.appendChild(kind);

    var actions = document.createElement("div");
    actions.className = "pop-actions";
    actions.appendChild(button("Sort ↑", function () { closePopover(); setSort(index, "asc"); }));
    actions.appendChild(button("Sort ↓", function () { closePopover(); setSort(index, "desc"); }));
    actions.appendChild(button("Filter…", function () {
      closePopover();
      search.value = quoteTerm(col.name) + ":";
      search.focus();
    }));
    popover.appendChild(actions);

    var loading = document.createElement("div");
    loading.className = "muted";
    loading.textContent = "Reading the column…";
    popover.appendChild(loading);

    api("/api/stats", { col: index, q: state.q }).then(function (s) {
      if (popoverFor !== index) { return; }
      loading.remove();
      renderStats(popover, s, index);
      positionNear(popover, anchor);
    }).catch(function (err) {
      loading.textContent = String(err.message || err);
    });
  }

  function quoteTerm(s) { return /\s/.test(s) ? '"' + s + '"' : s; }

  function button(label, fn) {
    var b = document.createElement("button");
    b.type = "button";
    b.className = "ghostbtn";
    b.textContent = label;
    b.addEventListener("click", fn);
    return b;
  }

  function renderStats(host, s, index) {
    var gridEl = document.createElement("div");
    gridEl.className = "statgrid";
    addStat(gridEl, "Rows", group(s.rows));
    addStat(gridEl, "Filled", group(s.filled));
    addStat(gridEl, "Empty", group(s.empty));
    addStat(gridEl, "Distinct", group(s.distinct) + (s.distinctCapped ? "+" : ""));
    if (s.num) {
      addStat(gridEl, "Min", s.num.minText);
      addStat(gridEl, "Max", s.num.maxText);
      if (s.kind !== "date") {
        addStat(gridEl, "Mean", fmt(s.num.mean));
        addStat(gridEl, "Median", fmt(s.num.median));
        addStat(gridEl, "Sum", fmt(s.num.sum));
        addStat(gridEl, "Std dev", fmt(s.num.stddev));
      }
    }
    if (s.unparsed) { addStat(gridEl, "Unreadable", group(s.unparsed)); }
    host.appendChild(gridEl);

    if (s.num && s.num.hist && s.num.hist.length) {
      var head = document.createElement("div");
      head.className = "pop-section";
      head.textContent = "Distribution";
      host.appendChild(head);

      var peak = Math.max.apply(null, s.num.hist) || 1;
      var hist = document.createElement("div");
      hist.className = "hist";
      s.num.hist.forEach(function (n) {
        var bar = document.createElement("i");
        bar.style.height = (n === 0 ? 0 : Math.max(2, Math.round((n / peak) * 46))) + "px";
        bar.title = group(n) + (n === 1 ? " row" : " rows");
        hist.appendChild(bar);
      });
      host.appendChild(hist);

      var axis = document.createElement("div");
      axis.className = "hist-axis";
      var lo = document.createElement("span");
      lo.textContent = s.num.minText;
      var hi = document.createElement("span");
      hi.textContent = s.num.maxText;
      axis.appendChild(lo);
      axis.appendChild(hi);
      host.appendChild(axis);
    }

    if (s.top && s.top.length) {
      var head2 = document.createElement("div");
      head2.className = "pop-section";
      head2.textContent = "Most common";
      host.appendChild(head2);

      var list = document.createElement("div");
      list.className = "topvals";
      var peak2 = s.top[0].count || 1;
      s.top.forEach(function (item) {
        var row = document.createElement("div");
        row.className = "topval";
        row.title = "Filter to this value";

        var bar = document.createElement("span");
        bar.className = "bar";
        bar.style.width = Math.round((item.count / peak2) * 100) + "%";
        row.appendChild(bar);

        var v = document.createElement("span");
        v.className = "v";
        v.textContent = item.value;
        row.appendChild(v);

        var n = document.createElement("span");
        n.className = "n";
        n.textContent = group(item.count);
        row.appendChild(n);

        row.addEventListener("click", function () {
          closePopover();
          search.value = quoteTerm(state.columns[index].name) + ":" + quoteTerm(item.value);
          search.dispatchEvent(new Event("input"));
        });
        list.appendChild(row);
      });
      host.appendChild(list);
    }
  }

  function addStat(host, label, value) {
    var d = document.createElement("div");
    var a = document.createElement("span");
    a.textContent = label;
    var b = document.createElement("span");
    b.textContent = value;
    d.appendChild(a);
    d.appendChild(b);
    host.appendChild(d);
  }

  function fmt(v) {
    if (v === null || v === undefined || !isFinite(v)) { return "-"; }
    var abs = Math.abs(v);
    var s;
    if (abs >= 1e15 || (abs > 0 && abs < 1e-4)) { s = v.toPrecision(6); }
    else if (Number.isInteger(v)) { s = String(v); }
    else if (abs >= 100) { s = v.toFixed(2); }
    else if (abs >= 1) { s = v.toFixed(3); }
    else { s = v.toFixed(4); }
    if (s.indexOf(".") >= 0) { s = s.replace(/0+$/, "").replace(/\.$/, ""); }
    var parts = s.split(".");
    parts[0] = parts[0].replace(/\B(?=(\d{3})+(?!\d))/g, " ");
    return parts.join(".");
  }

  function positionNear(el, anchor) {
    var r = anchor.getBoundingClientRect();
    el.style.visibility = "hidden";
    el.hidden = false;
    var w = el.offsetWidth, h = el.offsetHeight;
    var left = Math.min(Math.max(8, r.left), window.innerWidth - w - 8);
    var top = r.bottom + 6;
    if (top + h > window.innerHeight - 8) {
      top = Math.max(8, window.innerHeight - h - 8);
    }
    el.style.left = left + "px";
    el.style.top = top + "px";
    el.style.visibility = "";
  }

  document.addEventListener("mousedown", function (ev) {
    var inside = function (sel) { return ev.target.closest ? ev.target.closest(sel) : null; };
    if (!popover.hidden && !popover.contains(ev.target) && !inside(".th-menu")) {
      closePopover();
    }
    if (!sheet.hidden && !sheet.contains(ev.target) && !inside("#options-btn")) {
      closeSheet();
    }
  });

  /* ---- options sheet -------------------------------------------------- */

  function closeSheet() { sheet.hidden = true; }

  function openSheet(anchor) {
    if (!sheet.hidden) { closeSheet(); return; }
    closePopover();
    sheet.innerHTML = "";

    var o = boot.options || {};
    sheet.appendChild(select("Delimiter", [
      ["", "Detected: " + (boot.delimiterName || "-")],
      [",", "Comma  ,"],
      [";", "Semicolon  ;"],
      ["\t", "Tab"],
      ["|", "Pipe  |"],
      [":", "Colon  :"]
    ], o.delimiter || "", function (v) { navigateWith({ d: v }); }));

    sheet.appendChild(select("First row", [
      ["auto", "Detected: " + (boot.hasHeader ? "column names" : "data")],
      ["yes", "Column names"],
      ["no", "Data"]
    ], o.header || "auto", function (v) { navigateWith({ header: v === "auto" ? "" : v }); }));

    var encOptions = [["", "Detected: " + (boot.encoding || "-")]];
    (boot.encodings || []).forEach(function (e) {
      if (e !== "auto") { encOptions.push([e, e]); }
    });
    sheet.appendChild(select("Text encoding", encOptions, o.encoding || "", function (v) {
      navigateWith({ enc: v });
    }));

    var meta = document.createElement("div");
    meta.className = "meta";
    metaRow(meta, "Size", boot.sizeText || "");
    metaRow(meta, "Modified", boot.modified || "");
    metaRow(meta, "Encoding", boot.encoding || "");
    metaRow(meta, "csv.localhost", boot.version || "");
    sheet.appendChild(meta);

    sheet.hidden = false;
    positionNear(sheet, anchor);
  }

  function metaRow(host, label, value) {
    var d = document.createElement("div");
    var a = document.createElement("span");
    a.textContent = label;
    var b = document.createElement("span");
    b.textContent = value;
    d.appendChild(a);
    d.appendChild(b);
    host.appendChild(d);
  }

  function select(label, options, current, onChange) {
    var wrap = document.createElement("label");
    var span = document.createElement("span");
    span.textContent = label;
    wrap.appendChild(span);

    var sel = document.createElement("select");
    options.forEach(function (pair) {
      var opt = document.createElement("option");
      opt.value = pair[0];
      opt.textContent = pair[1];
      if (pair[0] === current) { opt.selected = true; }
      sel.appendChild(opt);
    });
    sel.addEventListener("change", function () { onChange(sel.value); });
    wrap.appendChild(sel);
    return wrap;
  }

  // Re-parsing happens on the server, so a changed option is a fresh URL.
  function navigateWith(changes) {
    var p = new URLSearchParams(window.location.search);
    for (var k in changes) {
      if (changes[k]) { p.set(k, changes[k]); } else { p.delete(k); }
    }
    var qs = p.toString();
    window.location.search = qs ? "?" + qs : "";
  }

  document.getElementById("options-btn").addEventListener("click", function (ev) {
    ev.stopPropagation();
    openSheet(this);
  });

  /* ---- freeze, print -------------------------------------------------- */

  var freezeBtn = document.getElementById("freeze-btn");

  function applyFreeze() {
    table.classList.toggle("freeze", state.freeze);
    freezeBtn.classList.toggle("on", state.freeze);
  }

  freezeBtn.addEventListener("click", function () {
    state.freeze = !state.freeze;
    try { localStorage.setItem("csvl.freeze", state.freeze ? "1" : "0"); } catch (e) { /* ignore */ }
    applyFreeze();
  });

  document.getElementById("print-btn").addEventListener("click", function () {
    var wanted = Math.min(state.total, PRINT_MAX);
    if (wanted === 0) { window.print(); return; }
    var btn = this;
    btn.disabled = true;
    api("/api/rows", {
      offset: 0,
      limit: wanted,
      sort: state.sort >= 0 ? state.sort : "",
      dir: state.dir,
      q: state.q
    }).then(function (page) {
      var keep = tbody.cloneNode(true);
      var frag = document.createDocumentFragment();
      page.rows.forEach(function (cells, i) {
        var tr = document.createElement("tr");
        var num = document.createElement("td");
        num.className = "rownum";
        num.textContent = page.ids[i];
        tr.appendChild(num);
        state.columns.forEach(function (col, c) {
          var td = document.createElement("td");
          if (isNumericKind(col.kind)) { td.className = "num"; }
          td.textContent = cells[c] || "";
          tr.appendChild(td);
        });
        frag.appendChild(tr);
      });
      tbody.replaceChildren(frag);
      var restore = function () {
        tbody.replaceChildren.apply(tbody, Array.prototype.slice.call(keep.childNodes));
        render();
        window.removeEventListener("afterprint", restore);
        btn.disabled = false;
      };
      window.addEventListener("afterprint", restore);
      window.print();
      setTimeout(function () { if (!btn.disabled) { return; } restore(); }, 3000);
    }).catch(function () { btn.disabled = false; });
  });

  /* ---- live reload ---------------------------------------------------- */

  function flashBadge(text) {
    var badge = document.getElementById("reload-badge");
    badge.textContent = text || "updated";
    badge.hidden = false;
    clearTimeout(badge._t);
    badge._t = setTimeout(function () { badge.hidden = true; }, 1800);
  }

  function refreshFile() {
    api("/api/table", {}).then(function (fresh) {
      var columnsChanged = JSON.stringify(fresh.columns || []) !== JSON.stringify(state.columns);
      boot = Object.assign(boot, fresh);
      window.__CSVL = boot;
      state.columns = fresh.columns || [];
      showNotices();
      if (columnsChanged) {
        state.sort = -1;
        state.selected = null;
        hideInspector();
      }
      buildHeader();
      applyFreeze();
      reload(true);
      flashBadge("updated");
    }).catch(function () { window.location.reload(); });
  }

  function connectEvents() {
    if (!boot.eventsURL || !window.EventSource) { return; }
    var es = new EventSource(boot.eventsURL, { withCredentials: true });
    es.addEventListener("reload", function () { refreshFile(); });
    es.addEventListener("gone", function () { flashBadge("file removed"); });
  }

  /* ---- boot ----------------------------------------------------------- */

  grid.addEventListener("scroll", queueRender, { passive: true });
  window.addEventListener("resize", function () { sizeTable(); queueRender(); });

  try { state.freeze = localStorage.getItem("csvl.freeze") === "1"; } catch (e) { /* ignore */ }

  loadWidths();
  showNotices();

  if (!boot.error) {
    buildHeader();
    applyFreeze();
    state.seed = boot.firstPage || null;
    updateCounts();
    render();
    connectEvents();
    grid.focus();
  } else {
    counts.textContent = "";
  }
})();
