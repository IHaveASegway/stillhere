// stillhere's tables: click a column's header to sort by it, and type in a
// table's filter to show only the rows that hold the text. The page works
// without this script; it only reorders and hides rows the server sent.
"use strict";

// A cell as a number, when it reads as one: 1,204, 12.5%, -3, a rank such
// as 2–8 (its first number) or 1 of 1. Anything else, a dash for a value
// that is not known included, is not one.
function cellNumber(text) {
  const t = text.trim().replace(/,/g, "");
  let m = t.match(/^(-?\d*\.?\d+)%?$/);
  if (m) return parseFloat(m[1]);
  m = t.match(/^(\d+)(?:\s*[–-]\s*\d+| of \d+)$/);
  if (m) return parseFloat(m[1]);
  return null;
}

function sortBy(table, col, th) {
  const body = table.tBodies[0];
  if (!body) return;
  const descending = th.getAttribute("aria-sort") !== "descending";
  for (const other of table.tHead.rows[0].cells) other.removeAttribute("aria-sort");
  th.setAttribute("aria-sort", descending ? "descending" : "ascending");
  const rows = Array.from(body.rows);
  const key = (row) => (row.cells[col] ? row.cells[col].textContent : "");
  rows.sort((a, b) => {
    const x = key(a), y = key(b);
    const nx = cellNumber(x), ny = cellNumber(y);
    // Unknown values sort last either way.
    if (nx === null && ny !== null) return 1;
    if (ny === null && nx !== null) return -1;
    let c = nx !== null ? nx - ny : x.localeCompare(y, undefined, { numeric: true, sensitivity: "base" });
    if (nx === null && ny === null) {
      const ex = x.trim() === "" || x.trim() === "—", ey = y.trim() === "" || y.trim() === "—";
      if (ex !== ey) return ex ? 1 : -1;
    }
    return descending ? -c : c;
  });
  for (const row of rows) body.appendChild(row);
}

function filterRows(table, text) {
  const want = text.trim().toLowerCase();
  for (const row of table.tBodies[0].rows) {
    row.hidden = want !== "" && !row.textContent.toLowerCase().includes(want);
  }
}

document.addEventListener("DOMContentLoaded", () => {
  for (const box of document.querySelectorAll("div.table")) {
    const table = box.querySelector("table");
    if (!table || !table.tHead || !table.tBodies[0]) continue;
    Array.from(table.tHead.rows[0].cells).forEach((th, col) => {
      th.classList.add("sortable");
      th.tabIndex = 0;
      th.addEventListener("click", () => sortBy(table, col, th));
      th.addEventListener("keydown", (e) => {
        if (e.key === "Enter" || e.key === " ") { e.preventDefault(); sortBy(table, col, th); }
      });
    });
    const tools = box.querySelector(".tools");
    if (tools && table.tBodies[0].rows.length > 10) {
      const input = document.createElement("input");
      input.type = "search";
      input.className = "filter";
      input.placeholder = "Filter rows";
      input.setAttribute("aria-label", "Filter this table's rows");
      input.addEventListener("input", () => filterRows(table, input.value));
      tools.prepend(input);
    }
  }
});
