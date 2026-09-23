"use strict";

const $ = (selector) => document.querySelector(selector);
const $$ = (selector) => [...document.querySelectorAll(selector)];
const escapeHTML = (value) =>
  String(value ?? "").replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
const icon = (name) =>
  `<svg class="icon" aria-hidden="true"><use href="#i-${name}"/></svg>`;
const number = (value, digits = 2) =>
  new Intl.NumberFormat("ru-RU", { maximumFractionDigits: digits }).format(
    value,
  );
const date = (value) =>
  new Intl.DateTimeFormat("ru-RU", {
    day: "numeric",
    month: "short",
    timeZone: "UTC",
  }).format(new Date(`${value}T00:00:00Z`));
const today = () => new Date().toISOString().slice(0, 10);
const shiftDate = (value, days) => {
  const d = new Date(`${value}T00:00:00Z`);
  d.setUTCDate(d.getUTCDate() + days);
  return d.toISOString().slice(0, 10);
};
const state = {
  page: 1,
  pageSize: 100,
  key: "",
  snapshot: null,
  etag: null,
  result: null,
  busy: false,
  view: "overview",
  filter: "needed",
  search: "",
  supplier: "",
  pendingImport: null,
  params: {
    as_of: today(),
    lookback_days: 90,
    review_period_days: 14,
    safety_stock_days: 7,
  },
};
const warningLabels = {
  lead_time_unconfirmed: "Подтвердите срок нового заказа у поставщика",
  stock_unverified: "Нет подтверждённого остатка по этому коду 1С",
  stock_snapshot_outdated:
    "Дата остатка не соответствует дате расчёта; нужен актуальный снимок",
  missing_order_rules:
    "Минимум или кратность заказа отсутствует либо содержит ошибку Excel",
  conflicting_order_rules: "В источнике противоречивые ограничения заказа",
  supplier_article_conflict:
    "Одному коду 1С соответствуют разные артикулы поставщика",
  supplier_article_missing: "Нет подтверждённого артикула поставщика",
  purchase_unit_conversion_unconfirmed:
    "Закупка бухтами, учёт метрами: подтвердите пересчёт единиц",
  unit_conflict: "Источники используют разные единицы измерения",
  stock_reservation_mismatch:
    "Свободный остаток не совпадает с остатком минус резерв",
  negative_stock: "Источник содержит отрицательный остаток",
  invalid_current_stock: "Некорректный текущий остаток или резерв",
  incomplete_sales_window: "Расчёт ограничен доступным периодом истории",
  history_unavailable: "Для выбранного периода нет полной истории",
  insufficient_positive_days_for_spike_detection:
    "Мало дней с продажами для надёжной фильтрации всплесков",
  no_regular_demand: "В выбранном периоде нет регулярного спроса",
  overdue_shipments_excluded: "Просроченные поставки исключены из расчёта",
  insufficient_supply_during_lead_time:
    "Есть риск дефицита до поступления нового заказа",
};
let toastTimer;
let importRequest = 0;
let xlsxImport = null;
const mobileLayout = window.matchMedia("(max-width: 680px)");

function syncSidebar() {
  $("#sidebar").inert =
    mobileLayout.matches && !$("#sidebar").classList.contains("open");
}

mobileLayout.addEventListener("change", syncSidebar);

function toast(message) {
  $("#toast").textContent = message;
  $("#toast").hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    $("#toast").hidden = true;
  }, 4500);
}

function clearError() {
  $("#error-banner").hidden = true;
}
function showError(error) {
  const message =
    error.status === 401
      ? "Для доступа к данным введите ключ подключения."
      : error.message;
  $("#error-banner").innerHTML =
    `${escapeHTML(message)} <button data-action="${error.status === 401 ? "connect" : "refresh"}">${error.status === 401 ? "Подключиться" : "Обновить данные"}</button>`;
  $("#error-banner").hidden = false;
  if (error.status === 401) {
    $("#connection-label").textContent = "Нужен ключ доступа";
    $("#connection-dot").classList.add("offline");
  }
}

async function api(path, options = {}) {
  const { timeout = 30000, signal, ...requestOptions } = options;
  const headers = new Headers(requestOptions.headers);
  if (state.key) headers.set("Authorization", `Bearer ${state.key}`);
  if (requestOptions.body && !(requestOptions.body instanceof FormData))
    headers.set("Content-Type", "application/json");
  let response;
  try {
    response = await fetch(`/api/v1/${path}`, {
      ...requestOptions,
      headers,
      signal: signal
        ? AbortSignal.any([signal, AbortSignal.timeout(timeout)])
        : AbortSignal.timeout(timeout),
    });
  } catch {
    throw new Error(
      "Не удалось связаться с сервером. Проверьте подключение и повторите попытку.",
    );
  }
  if (!response.ok) {
    const body = await response.json().catch(() => null);
    const error = new Error(
      body?.error?.message ||
        `Ошибка сервера (${response.status}). Повторите попытку.`,
    );
    error.status = response.status;
    throw error;
  }
  return response;
}

function busy(value) {
  state.busy = value;
  $("#loading").hidden = !value;
  $("#main").setAttribute("aria-busy", String(value));
  $$(
    "[data-action='calculate'], [data-action='import'], [data-action='settings'], [data-action='refresh'], [data-action='connect'], [data-action='demo']",
  ).forEach((el) => {
    el.disabled = value;
  });
  $("#calculate-button").disabled =
    value || !state.snapshot?.data.products.length;
  $("#export-button").disabled =
    value || !state.result?.orders.length || state.view === "shipments";
}

async function getSnapshot() {
  const response = await api("dataset");
  const snapshot = await response.json();
  if (!state.snapshot && snapshot.data.source?.as_of)
    state.params.as_of = shiftDate(snapshot.data.source.as_of, 1);
  state.snapshot = snapshot;
  state.etag = response.headers.get("ETag");
  state.result = null;
  $("#connection-label").textContent = "Сервер подключён";
  $("#connection-dot").classList.remove("offline");
}

async function getRecommendations() {
  const response = await api("recommendations", {
    method: "POST",
    body: JSON.stringify(state.params),
  });
  const result = await response.json();
  if (result.dataset_revision !== state.snapshot.revision) {
    throw new Error(
      "Данные изменились во время расчёта. Обновите страницу данных и повторите расчёт.",
    );
  }
  state.result = result;
}

async function refresh() {
  if (state.busy) return;
  busy(true);
  clearError();
  try {
    await getSnapshot();
    if (state.snapshot.data.products.length) await getRecommendations();
  } catch (error) {
    showError(error);
  } finally {
    render();
    busy(false);
  }
}

async function calculate() {
  if (state.busy || !state.snapshot) return;
  busy(true);
  clearError();
  state.result = null;
  try {
    await getSnapshot();
    await getRecommendations();
    toast("Рекомендации обновлены");
  } catch (error) {
    showError(error);
  } finally {
    render();
    busy(false);
  }
}

function renderStats() {
  const lines = state.result?.products || [];
  const needed = lines.filter((line) => line.order_quantity > 0);
  const spikes = lines.reduce(
    (sum, line) =>
      sum + line.adjustments.filter((a) => a.reason === "sales_spike").length,
    0,
  );
  const risk = lines.filter((line) =>
    line.warnings.includes("insufficient_supply_during_lead_time"),
  ).length;
  const items = [
    {
      label: "Товаров к закупке",
      value: needed.length,
      unit: "позиций",
      caption: `из ${number(state.snapshot?.data.products.length || 0)} товаров на складе`,
      icon: "box",
      color: "",
    },
    {
      label: "Заказов поставщикам",
      value: state.result?.orders.length || 0,
      unit: "заказов",
      caption: "Сгруппированы по поставщикам",
      icon: "truck",
      color: "blue",
    },
    {
      label: "Всплесков исключено",
      value: spikes,
      unit: "дней",
      caption: "Учитываем регулярную потребность",
      icon: "spark",
      color: "green",
    },
    {
      label: "Риск дефицита",
      value: risk,
      unit: "позиций",
      caption: "Требуют внимания до поставки",
      icon: "chart",
      color: "orange",
    },
  ];
  $("#stats").innerHTML = items
    .map(
      (s) =>
        `<article class="stat-card"><div class="stat-header"><span>${s.label}</span><span class="stat-icon ${s.color}">${icon(s.icon)}</span></div><div class="stat-number">${state.result ? number(s.value) : "—"}<small>${s.unit}</small></div><div class="stat-caption">${s.color === "green" ? icon("check") : ""}${s.caption}</div></article>`,
    )
    .join("");
  $("#nav-count").textContent = needed.length;
  $("#needed-count").textContent = needed.length;
}

function renderChart() {
  const result = state.result;
  $("#chart-period").textContent = `${state.params.lookback_days} дней`;
  if (!result) {
    $("#insight-description").textContent =
      "Учитываем историю продаж, остатки и товары в пути, чтобы предложить обоснованную закупку.";
    $("#demand-chart").innerHTML =
      `<div class="chart-empty">${icon("chart")}<br>Ваша история продаж станет понятным графиком.<br>Загрузите данные, чтобы увидеть динамику спроса.</div>`;
    return;
  }
  const size = Math.max(
    0,
    Math.round(
      (new Date(result.history_end_exclusive) -
        new Date(result.history_start)) /
        86400000,
    ),
  );
  if (!size) {
    $("#demand-chart").innerHTML =
      '<div class="chart-empty">Нет истории за выбранный период</div>';
    return;
  }
  const start = result.history_start;
  const points = Array.from({ length: size }, (_, i) => ({
    date: shiftDate(start, i),
    raw: 0,
    adjusted: 0,
  }));
  const indices = new Map(points.map((p, i) => [p.date, i]));
  const adjustments = new Map(
    result.products.flatMap((line) =>
      line.adjustments.map((a) => [
        JSON.stringify([line.product_id, a.date]),
        a.used_quantity,
      ]),
    ),
  );
  for (const sale of state.snapshot.data.sales) {
    const index = indices.get(sale.date);
    if (index === undefined) continue;
    const key = JSON.stringify([sale.product_id, sale.date]);
    points[index].raw += sale.quantity;
    points[index].adjusted += adjustments.has(key)
      ? adjustments.get(key)
      : sale.quantity;
  }
  const max = Math.max(1, ...points.map((p) => p.raw));
  const top = Math.ceil(max / 4) * 4;
  const low = Math.min(0, ...points.map((p) => p.raw));
  const width = Math.max(280, $("#demand-chart").clientWidth - 36),
    height = 165,
    left = 40,
    right = 10,
    bottom = 26,
    yTop = 8;
  const x = (i) => left + (i / Math.max(1, size - 1)) * (width - left - right);
  const y = (v) =>
    height - bottom - ((v - low) / (top - low)) * (height - bottom - yTop);
  const path = (key) =>
    points
      .map(
        (p, i) => `${i ? "L" : "M"}${x(i).toFixed(2)},${y(p[key]).toFixed(2)}`,
      )
      .join(" ");
  const grid = Array.from({ length: 5 }, (_, i) => {
    const value = low + ((top - low) * i) / 4;
    return `<line class="grid" x1="${left}" y1="${y(value)}" x2="${width - right}" y2="${y(value)}"/><text x="${left - 10}" y="${y(value) + 3}" text-anchor="end">${number(value)}</text>`;
  }).join("");
  const labels = Array.from(
    new Set(
      Array.from({ length: Math.min(6, size) }, (_, i) =>
        Math.round((i * (size - 1)) / Math.max(1, Math.min(6, size) - 1)),
      ),
    ),
  )
    .map(
      (i) =>
        `<text x="${x(i)}" y="${height - 6}" text-anchor="${i === 0 ? "start" : i === size - 1 ? "end" : "middle"}">${date(points[i].date)}</text>`,
    )
    .join("");
  const rawTotal = points.reduce((sum, p) => sum + p.raw, 0);
  const regularTotal = points.reduce((sum, p) => sum + p.adjusted, 0);
  $("#demand-chart").innerHTML =
    `<svg viewBox="0 0 ${width} ${height}" role="img" aria-labelledby="chart-title chart-desc"><title id="chart-title">Продажи и скорректированный спрос за ${size} дней</title><desc id="chart-desc">Суммарные продажи ${number(rawTotal)} базовых единиц; скорректированный спрос ${number(regularTotal, 1)}. График объединяет базовые единицы разных товаров.</desc>${grid}<path class="area" d="${path("adjusted")} L${x(size - 1)},${y(0)} L${left},${y(0)} Z"/><path class="raw-line" d="${path("raw")}"/><path class="regular-line" d="${path("adjusted")}"/>${labels}</svg>`;
  $("#insight-description").textContent =
    rawTotal > regularTotal
      ? `Исключили ${number(rawTotal - regularTotal, 1)} базовых единиц нерегулярного спроса. Разовые продажи не приведут к избыточной закупке.`
      : "Учитываем историю продаж, остатки и товары в пути, чтобы предложить обоснованную закупку.";
}

function emptyTable() {
  const hasData = !!state.snapshot?.data.products.length;
  if (!state.snapshot)
    return `<div class="empty-state"><div class="empty-symbol">${icon("key")}</div><h3>Подключитесь к рабочему пространству</h3><p>После подключения здесь появятся данные склада и рекомендации по закупке.</p><button class="button primary" data-action="connect">Настроить подключение</button></div>`;
  if (!hasData)
    return `<div class="empty-state"><div class="empty-symbol">${icon("box")}</div><h3>Хороший план начинается с ваших данных</h3><p>Загрузите продажи, остатки и поставки. Мы рассчитаем, какие товары и в каком количестве пора заказать.</p><button class="button primary" data-action="import">${icon("upload")}Загрузить данные</button><button class="button secondary" data-action="demo">Попробовать демо</button></div>`;
  if (!state.result)
    return `<div class="empty-state"><div class="empty-symbol">${icon("refresh")}</div><h3>Нужен новый расчёт</h3><p>Данные загружены. Рассчитайте закупку, чтобы увидеть актуальные предложения.</p><button class="button primary" data-action="calculate">Рассчитать закупку</button></div>`;
  if (
    state.result?.products.some((p) => p.blocked) &&
    state.filter === "needed"
  )
    return '<div class="empty-state"><h3>Нужна проверка исходных данных</h3><p>Часть товаров исключена из заказов из-за неподтверждённых остатков, сроков или ограничений. Откройте вкладку «Требуют внимания».</p><button class="button secondary" data-filter="attention">Требуют внимания</button></div>';
  return `<div class="empty-state"><div class="empty-symbol">${icon("check")}</div><h3>${state.filter === "needed" && !state.search && !state.supplier ? "Запасов достаточно" : "Подходящих товаров нет"}</h3><p>${state.filter === "needed" && !state.search && !state.supplier ? "В выбранном горизонте пополнение не требуется. Все товары доступны на вкладке «Все товары»." : "Попробуйте другой поисковый запрос, поставщика или фильтр."}</p></div>`;
}

function renderTable() {
  const supplierMap = new Map(
    (state.snapshot?.data.suppliers || []).map((s) => [s.id, s.name]),
  );
  if (state.view === "shipments") {
    renderShipments(supplierMap);
    return;
  }
  let lines = state.result?.products || [];
  if (state.filter === "needed")
    lines = lines.filter((line) => line.order_quantity > 0);
  if (state.filter === "attention")
    lines = lines.filter(
      (line) => line.warnings.length > 0 || line.adjustments.length > 0,
    );
  lines = lines.filter(
    (line) =>
      (!state.supplier || line.supplier_id === state.supplier) &&
      `${line.name} ${line.sku} ${line.internal_code || ""}`
        .toLocaleLowerCase("ru-RU")
        .includes(state.search),
  );
  lines.sort(
    (a, b) =>
      (supplierMap.get(a.supplier_id) || "").localeCompare(
        supplierMap.get(b.supplier_id) || "",
        "ru",
      ) || a.name.localeCompare(b.name, "ru"),
  );
  $("#table-count").textContent = lines.length;
  $("#result-caption").textContent = state.result
    ? `Показано ${number(lines.length)} из ${number(state.result.products.length)} товаров · ${date(state.result.parameters.as_of)}`
    : "Загрузите данные для начала работы";
  const pageCount = Math.max(1, Math.ceil(lines.length / state.pageSize));
  state.page = Math.min(state.page, pageCount);
  $("#pagination").hidden = pageCount <= 1;
  $("#pagination").innerHTML =
    `<button class="button secondary compact" data-action="previous-page" ${state.page === 1 ? "disabled" : ""}>←</button><span>${state.page} / ${pageCount}</span><button class="button secondary compact" data-action="next-page" ${state.page === pageCount ? "disabled" : ""}>→</button>`;
  const total = lines.length;
  lines = lines.slice(
    (state.page - 1) * state.pageSize,
    state.page * state.pageSize,
  );
  if (state.result)
    $("#result-caption").textContent =
      `Показано ${lines.length} из ${total} · ${date(state.result.parameters.as_of)}`;
  if (!lines.length) {
    $("#table-content").innerHTML = emptyTable();
    return;
  }
  $("#table-content").innerHTML =
    `<div class="table-scroll"><table><thead><tr><th scope="col">Товар / Артикул</th><th scope="col">Поставщик</th><th scope="col">Прогноз / день</th><th scope="col">Доступно</th><th scope="col">В пути</th><th scope="col">К закупке</th><th scope="col">Статус</th><th scope="col"><span class="muted">Расчёт</span></th></tr></thead><tbody>${lines
      .map((line) => {
        const risk = line.warnings.includes(
          "insufficient_supply_during_lead_time",
        );
        const adjusted = line.adjustments.length > 0;
        const status = line.blocked
          ? ["orange", "Нужна проверка"]
          : risk
            ? ["orange", "Риск дефицита"]
            : adjusted
              ? ["purple", "Спрос скорректирован"]
              : line.order_quantity > 0
                ? ["purple", "К закупке"]
                : ["green", "Запас в норме"];
        return `<tr><td><div class="product-cell"><span class="product-icon">${icon("box")}</span><div><div class="product-name">${escapeHTML(line.name)}</div><div class="product-sku">${escapeHTML(line.sku)}${line.internal_code ? ` · 1С: ${escapeHTML(line.internal_code)}` : ""}${line.unit ? ` · ${escapeHTML(line.unit)}` : ""}</div></div></div></td><td>${escapeHTML(supplierMap.get(line.supplier_id))}</td><td class="numeric">${number(line.forecast_daily_demand ?? line.daily_demand, 2)}</td><td class="numeric">${number(line.available_stock)}</td><td class="numeric">${number(line.incoming_quantity)}</td><td class="order-quantity numeric">${line.order_quantity ? number(line.order_quantity) : "—"}</td><td><span class="badge ${status[0]}">${status[1]}</span></td><td><button class="row-detail" data-detail="${escapeHTML(line.product_id)}" aria-label="Расчёт для ${escapeHTML(line.name)}">${icon("chevron")}</button></td></tr>`;
      })
      .join("")}</tbody></table></div>`;
}

function renderShipments(supplierMap) {
  $("#pagination").hidden = true;
  const products = new Map(
    (state.snapshot?.data.products || []).map((p) => [p.id, p]),
  );
  const shipments = (state.snapshot?.data.shipments || [])
    .filter((s) => {
      const p = products.get(s.product_id);
      return (
        p &&
        (!state.supplier || p.supplier_id === state.supplier) &&
        `${p.name} ${p.sku} ${p.internal_code || ""}`
          .toLocaleLowerCase("ru-RU")
          .includes(state.search)
      );
    })
    .sort((a, b) => a.expected_date.localeCompare(b.expected_date));
  $("#table-count").textContent = shipments.length;
  $("#result-caption").textContent =
    `Открытых поставок: ${number(shipments.length)}`;
  $("#table-content").innerHTML = shipments.length
    ? `<div class="table-scroll"><table><thead><tr><th scope="col">Товар / Артикул</th><th scope="col">Поставщик</th><th scope="col">Количество</th><th scope="col">Ожидаемая дата</th><th scope="col">Статус</th></tr></thead><tbody>${shipments
        .map((s) => {
          const p = products.get(s.product_id);
          const overdue = s.expected_date < state.params.as_of;
          return `<tr><td><div class="product-name">${escapeHTML(p.name)}</div><div class="product-sku">${escapeHTML(p.sku)}</div></td><td>${escapeHTML(supplierMap.get(p.supplier_id))}</td><td class="numeric">${number(s.quantity)}</td><td>${date(s.expected_date)}</td><td><span class="badge ${overdue ? "orange" : "green"}">${overdue ? "Просрочена" : "В пути"}</span></td></tr>`;
        })
        .join(
          "",
        )}</tbody></table></div><div class="shipments-summary">Статусы на ${date(state.params.as_of)}. При получении поставки обновите остатки и уменьшите количество в пути в одном импорте.</div>`
    : `<div class="empty-state"><div class="empty-symbol">${icon("truck")}</div><h3>Открытых поставок нет</h3><p>Поставки появятся после загрузки данных или изменения фильтров.</p></div>`;
}

function render() {
  const source = state.snapshot?.data.source;
  const blocked = (state.result?.products || []).filter(
    (p) => p.blocked,
  ).length;
  $("#source-notice").hidden = !source?.label && !blocked;
  $("#source-notice").innerHTML =
    `<strong>${escapeHTML(source?.label || "Проверка данных")}</strong>${source?.as_of ? ` · Источники на ${date(source.as_of)}` : ""}<p>${blocked ? `${blocked} товаров требуют проверки и исключены из экспорта. ` : ""}Месячные отчёты сверены с операциями; для спроса используются дневные продажи.</p>${(
      source?.warnings || []
    )
      .filter(
        (w) =>
          !w.startsWith("Подтвердите сроки") ||
          state.snapshot.data.suppliers.some(
            (supplier) => supplier.lead_time_unconfirmed,
          ),
      )
      .map((w) => `<p>${escapeHTML(w)}</p>`)
      .join(
        "",
      )}<button class="text-button" data-action="settings">Настроить сроки поставки</button>`;
  $("#planning-date").textContent = date(state.params.as_of);
  $("#history-label").textContent = `${state.params.lookback_days} дней`;
  $("#overview-section").hidden = state.view !== "overview";
  const titles = {
    overview: [
      "Пополнение склада",
      "Обзор закупок",
      "Нужные товары. В нужном количестве. Вовремя.",
    ],
    orders: [
      "Рекомендации к закупке",
      "Рекомендации",
      "Обоснованные заказы для каждого поставщика.",
    ],
    inventory: [
      "Товары и остатки",
      "Товары и остатки",
      "Спрос, доступный запас и потребность в пополнении.",
    ],
    shipments: [
      "Товары в пути",
      "Товары в пути",
      "Открытые поставки и ожидаемые даты поступления.",
    ],
  };
  const [title, breadcrumb, description] = titles[state.view];
  $("#page-title").textContent = title;
  $("#breadcrumb-current").textContent = breadcrumb;
  $("#page-description").textContent = description;
  $("#table-title").textContent =
    state.view === "shipments"
      ? "Ожидаемые поставки"
      : state.view === "inventory"
        ? "Товары на складе"
        : "Рекомендации к закупке";
  $("#table-description").textContent =
    state.view === "shipments"
      ? "Неполученные товары из текущих заказов"
      : "Предложения по товарам, сгруппированные по поставщикам";
  $(".tabs").hidden = state.view === "shipments";
  $$("[data-view]").forEach((el) => {
    const active = el.dataset.view === state.view;
    el.classList.toggle("active", active);
    if (active) el.setAttribute("aria-current", "page");
    else el.removeAttribute("aria-current");
  });
  $$("[data-filter]").forEach((el) => {
    el.classList.toggle("active", el.dataset.filter === state.filter);
    el.setAttribute("aria-pressed", String(el.dataset.filter === state.filter));
  });
  const suppliers = state.snapshot?.data.suppliers || [];
  $("#supplier-filter").innerHTML =
    `<option value="">Все поставщики</option>${suppliers.map((s) => `<option value="${escapeHTML(s.id)}">${escapeHTML(s.name)}</option>`).join("")}`;
  if (!suppliers.some((s) => s.id === state.supplier)) state.supplier = "";
  $("#supplier-filter").value = state.supplier;
  $("#updated-at").textContent = state.snapshot?.revision
    ? `Данные обновлены ${new Intl.DateTimeFormat("ru-RU", { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" }).format(new Date(state.snapshot.updated_at))}`
    : "Данные ещё не загружены";
  renderStats();
  renderChart();
  renderTable();
  busy(state.busy);
}

function resetImport() {
  importRequest += 1;
  if (xlsxImport?.converting) {
    xlsxImport.controller.abort();
    busy(false);
  }
  xlsxImport = null;
  state.pendingImport = null;
}

function openModal(title, body, preserveImport = false) {
  if (!preserveImport) resetImport();
  $("#modal-title").textContent = title;
  $("#modal-body").innerHTML = body;
  if (!$("#modal").open) $("#modal").showModal();
}
function closeModal() {
  resetImport();
  $("#modal").close();
}
function modalError(error) {
  let el = $("#modal-error");
  if (!el) {
    el = document.createElement("p");
    el.id = "modal-error";
    el.className = "modal-error";
    el.setAttribute("role", "alert");
    $("#modal-body").append(el);
  }
  el.textContent = error.message;
  el.tabIndex = -1;
  el.focus();
}

function settings() {
  openModal(
    "Параметры расчёта",
    `<p class="modal-copy">Настройте период анализа и запас. Продажи за дату расчёта не учитываются, поскольку день ещё может быть неполным.</p><form id="settings-form"><div class="form-grid"><label class="field full">Дата расчёта<input name="as_of" type="date" value="${state.params.as_of}" min="1900-01-01" max="9990-12-31" required></label><label class="field">История продаж, дней<input name="lookback_days" type="number" min="7" max="730" step="1" value="${state.params.lookback_days}" required></label><label class="field">Период закупки, дней<input name="review_period_days" type="number" min="1" max="365" step="1" value="${state.params.review_period_days}" required></label><label class="field full">Страховой запас, дней<input name="safety_stock_days" type="number" min="0" max="365" step="1" value="${state.params.safety_stock_days}" required><small>Добавляется к сроку поставки и периоду закупки.</small></label>${(state.snapshot?.data.suppliers || []).map((supplier, index) => `<label class="field full">Срок ${escapeHTML(supplier.name)}, дней<input name="lead-${index}" type="number" min="0" max="365" step="1" value="${supplier.lead_time_unconfirmed ? "" : supplier.lead_time_days}" placeholder="Подтвердите срок нового заказа" required><small>${supplier.lead_time_unconfirmed ? "Не указан в источниках. Введите согласованный срок." : "Срок нового заказа, а не дата уже отгруженной поставки."}</small></label>`).join("")}</div><div class="modal-actions"><button class="button secondary" type="button" data-action="close-modal">Отмена</button><button class="button primary" type="submit">Применить и рассчитать</button></div></form>`,
  );
}

function connect() {
  openModal(
    "Подключение к складу",
    `<p class="modal-copy">Если сервер защищён, введите ключ доступа, полученный у администратора. Ключ хранится только в памяти этой вкладки и удаляется при перезагрузке.</p><form id="connect-form"><label class="field">Ключ доступа<input name="key" type="password" autocomplete="off" placeholder="Введите ключ доступа" aria-label="Ключ доступа"><small>Для локального сервера без защиты оставьте поле пустым.</small></label><div class="modal-actions"><button type="button" class="button secondary" data-action="close-modal">Отмена</button><button class="button primary" type="submit">Подключиться</button></div></form>`,
  );
}

function method() {
  openModal(
    "Как работает расчёт",
    `<p class="modal-copy">Прозрачная рекомендация на основе данных вашего склада.</p><ol class="method-list"><li><strong>Находим регулярный спрос.</strong> Анализируем полные календарные дни. Пропущенные дни считаются днями без продаж.</li><li><strong>Убираем разовые всплески.</strong> Сравниваем продажи с медианой и разбросом. При недостатке истории оставляем продажи и показываем предупреждение.</li><li><strong>Учитываем доступный запас.</strong> Вычитаем резервы, добавляем поставки в пределах горизонта. Просроченные поставки исключаем.</li><li><strong>Рассчитываем закупку.</strong> Покрываем срок поставки, период закупки и страховой запас. Округляем заказ до упаковки и минимальной партии.</li></ol><p class="notice">График суммирует базовые единицы разных товаров. Рекомендации рассчитываются отдельно по каждой позиции. При наличии профиля поставщика спрос корректируется по месячной сезонности. Временное отсутствие товара не восстанавливается по месячным остаткам. Неподтверждённые данные исключают позицию из экспорта.</p><div class="modal-actions"><button class="button primary" data-action="close-modal">Понятно</button></div>`,
  );
}

function showDetail(id) {
  const line = state.result?.products.find((p) => p.product_id === id);
  if (!line) return;
  const product = state.snapshot.data.products.find((p) => p.id === id);
  const stock = state.snapshot.data.stock.find((p) => p.product_id === id);
  const supplier = state.snapshot.data.suppliers.find(
    (s) => s.id === line.supplier_id,
  );
  const pairs = [
    ["Исходный спрос / день", number(line.raw_daily_demand, 2)],
    ["Регулярный спрос / день", number(line.daily_demand, 2)],
    [
      "Прогноз / день",
      number(line.forecast_daily_demand ?? line.daily_demand, 2),
    ],
    ["Поправка сезонности", number(line.seasonal_factor ?? 1, 3)],
    ["Дата остатка", stock.as_of ? date(stock.as_of) : "Не указана"],
    ["Горизонт покрытия", `${line.coverage_days} дн.`],
    ["Целевой запас", number(line.target_stock)],
    [
      "На складе / резерв",
      `${number(stock.on_hand)} / ${number(stock.reserved)}`,
    ],
    [
      "Ожидаемая поставка",
      date(shiftDate(state.result.parameters.as_of, supplier.lead_time_days)),
    ],
    ["Доступный остаток", number(line.available_stock)],
    ["Учтено в пути", number(line.incoming_quantity)],
    ["Чистая потребность", number(line.net_requirement)],
    [
      "Упаковка / минимум",
      `${number(product.pack_size)} / ${number(product.min_order_quantity)}`,
    ],
  ];
  openModal(
    line.name,
    `<p class="modal-copy">${escapeHTML(line.sku)} · Покрытие до ${date(line.coverage_end)}</p><div class="detail-grid">${pairs.map(([label, value]) => `<div class="detail-item"><span>${label}</span><strong>${value}</strong></div>`).join("")}</div><div class="detail-total"><span>${line.blocked ? "Черновик · нужна проверка" : "Рекомендовано к закупке"}</span><strong>${number(line.blocked ? line.suggested_quantity : line.order_quantity)}</strong></div>${line.adjustments.length ? `<h3 class="detail-subtitle">Корректировки продаж</h3>${line.adjustments.map((a) => `<div class="adjustment-row"><span>${date(a.date)} · ${a.reason === "sales_spike" ? "Всплеск" : a.reason === "net_returns" ? "Возврат" : "Ручное исключение"}</span><strong>${number(a.original_quantity)} → ${number(a.used_quantity, 1)}</strong></div>`).join("")}` : ""}${line.warnings.length ? `<h3 class="detail-subtitle">Обратите внимание</h3><ul class="warning-list">${line.warnings.map((w) => `<li>${escapeHTML(warningLabels[w] || w)}</li>`).join("")}</ul>` : ""}<div class="modal-actions"><button class="button primary" data-action="close-modal">Готово</button></div>`,
  );
}

function demoDataset() {
  const start = shiftDate(today(), -30);
  const suppliers = [
    { id: "electro", name: "Электропоставка", lead_time_days: 5 },
    { id: "light", name: "Светотехника", lead_time_days: 3 },
    { id: "volt", name: "ВольтМаркет", lead_time_days: 7 },
  ];
  const definitions = [
    [
      "cable",
      "ВВГнг 3×2.5",
      "Кабель силовой ВВГнг, м",
      "electro",
      20,
      50,
      10,
      90,
      10,
    ],
    [
      "breaker",
      "ВА47-29 / C16",
      "Автоматический выключатель 16А",
      "electro",
      4,
      6,
      6,
      240,
      5,
    ],
    [
      "lamp",
      "LED-A60-10W",
      "Лампа светодиодная 10Вт",
      "light",
      12,
      20,
      10,
      44,
      4,
    ],
    ["socket", "SO-16A-W", "Розетка с заземлением", "volt", 6, 10, 10, 100, 0],
    [
      "panel",
      "ЩРН-П-12",
      "Щит распределительный, 12 модулей",
      "electro",
      2,
      4,
      4,
      6,
      0,
    ],
    ["strip", "LED-12V-60", "Лента светодиодная, м", "light", 8, 25, 5, 80, 10],
  ];
  const data = { suppliers, products: [], sales: [], stock: [], shipments: [] };
  definitions.forEach(
    (
      [id, sku, name, supplier_id, daily, minimum, pack, on_hand, reserved],
      productIndex,
    ) => {
      data.products.push({
        id,
        sku,
        name,
        supplier_id,
        pack_size: pack,
        min_order_quantity: minimum,
      });
      data.stock.push({ product_id: id, on_hand, reserved });
      for (let day = 0; day < 30; day++) {
        const factor = [0.85, 1.1, 1, 0.9, 1.2, 0.75, 1.05][
          (day + productIndex) % 7
        ];
        let quantity = Math.max(1, Math.round(daily * factor));
        if (
          (id === "cable" && day === 11) ||
          (id === "lamp" && day === 21) ||
          (id === "strip" && day === 11)
        )
          quantity *= 8;
        data.sales.push({
          product_id: id,
          date: shiftDate(start, day),
          quantity,
          exclude_from_demand: false,
        });
      }
    },
  );
  data.shipments = [
    {
      id: "demo-cable",
      product_id: "cable",
      quantity: 100,
      expected_date: shiftDate(today(), 3),
    },
    {
      id: "demo-lamp",
      product_id: "lamp",
      quantity: 40,
      expected_date: shiftDate(today(), 2),
    },
    {
      id: "demo-socket",
      product_id: "socket",
      quantity: 20,
      expected_date: shiftDate(today(), -2),
    },
  ];
  return data;
}

function xlsxImportForm() {
  if (!xlsxImport) return;
  const { files, asOf, historyStart } = xlsxImport;
  state.pendingImport = null;
  openModal(
    "Загрузка файлов Excel",
    `<p class="modal-copy">Добавьте полный комплект из шести отчётов для ИЭК, Systeme Electric или обоих поставщиков. Можно выбирать файлы по очереди из разных папок, сохраняя исходные имена.</p><p class="notice">Для каждого поставщика нужны дневные продажи, месячные продажи, месячные остатки, товары в пути, минимальная партия или кратность заказа и сезонность. Файл минимальной партии ИЭК добавьте отдельно, если он лежит вне папки поставщика.</p><form id="xlsx-import-form"><div class="import-files-heading"><strong>Выбрано файлов: ${files.length} из 12</strong><button class="button secondary" type="button" data-action="add-import-files">${icon("upload")}Добавить файлы</button></div><ul class="import-files" id="xlsx-file-list">${files.length ? files.map((file, index) => `<li><span><strong>${escapeHTML(file.name)}</strong><small>${number(file.size / 1024 / 1024)} МБ</small></span><button class="icon-button" type="button" data-remove-import-file="${index}" aria-label="Удалить ${escapeHTML(file.name)}">${icon("close")}</button></li>`).join("") : '<li class="muted">Добавьте отчёты в формате .xlsx.</li>'}</ul><p class="import-size muted">Общий размер: ${number(files.reduce((sum, file) => sum + file.size, 0) / 1024 / 1024)} МБ из 64 МБ</p><div class="form-grid"><label class="field">Дата выгрузки<input name="as_of" type="date" value="${escapeHTML(asOf)}" min="1900-01-01" max="9990-12-31" required><small>Дата, на которую собраны отчёты. Должна совпадать с датой в именах файлов.</small></label><label class="field">Начало истории продаж<input name="history_start" type="date" value="${escapeHTML(historyStart)}" min="1900-01-01" max="${escapeHTML(asOf || "9990-12-31")}" required><small>Первый день полного периода выгрузки, включая дни без продаж.</small></label></div><p class="modal-copy import-help">После проверки вы увидите состав данных и предупреждения. Сохранение потребует отдельного подтверждения. Резервную копию JSON можно загрузить одним файлом.</p><p id="xlsx-import-status" class="import-progress" role="status" hidden><span class="spinner"></span>Читаем отчёты и проверяем данные…</p><div class="modal-actions"><button class="button secondary" type="button" data-action="close-modal">Отмена</button><button class="button primary" id="convert-xlsx" type="submit" ${files.length ? "" : "disabled"}>Проверить файлы</button></div></form>`,
    true,
  );
}

function rememberXlsxDates() {
  const form = $("#xlsx-import-form");
  if (!xlsxImport || !form) return;
  const fields = new FormData(form);
  xlsxImport.asOf = fields.get("as_of");
  xlsxImport.historyStart = fields.get("history_start");
}

async function convertXlsx(form) {
  if (state.busy || !xlsxImport || !form.reportValidity()) return;
  rememberXlsxDates();
  const current = xlsxImport;
  if (!current.files.length) {
    modalError(new Error("Добавьте отчёты в формате .xlsx."));
    return;
  }
  if (current.historyStart > current.asOf) {
    modalError(new Error("Начало истории не может быть позже даты выгрузки."));
    return;
  }
  const body = new FormData();
  current.files.forEach((file) => body.append("files", file));
  body.append("as_of", current.asOf);
  body.append("history_start", current.historyStart);
  current.controller = new AbortController();
  current.converting = true;
  busy(true);
  $("#modal-error")?.remove();
  form.setAttribute("aria-busy", "true");
  form
    .querySelectorAll("input, button:not([data-action='close-modal'])")
    .forEach((el) => {
      el.disabled = true;
    });
  $("#xlsx-import-status").hidden = false;
  try {
    const response = await api("import/xlsx", {
      method: "POST",
      body,
      timeout: 120000,
      signal: current.controller.signal,
    });
    const converted = await response.json();
    if (xlsxImport !== current || !$("#modal").open) return;
    importPreview(converted.data, `Excel · ${current.files.length} файлов`);
  } catch (error) {
    if (xlsxImport === current && $("#modal").open) modalError(error);
  } finally {
    if (xlsxImport === current) {
      current.converting = false;
      busy(false);
      if ($("#xlsx-import-form") === form) {
        form.setAttribute("aria-busy", "false");
        form.querySelectorAll("input, button").forEach((el) => {
          el.disabled = false;
        });
        $("#xlsx-import-status").hidden = true;
      }
    }
  }
}

function importPreview(data, name, demo = false) {
  if (
    !data ||
    ["suppliers", "products", "sales", "stock", "shipments"].some(
      (key) => !Array.isArray(data[key]),
    )
  )
    throw new Error(
      "Файл должен содержать массивы suppliers, products, sales, stock и shipments.",
    );
  if (demo) resetImport();
  state.pendingImport = { data, etag: state.etag, demo };
  openModal(
    demo ? "Попробуйте на примере склада" : "Проверка перед загрузкой",
    `<p class="modal-copy">${demo ? "Учебные данные: кабели, автоматика и освещение. История за последние 30 дней содержит три разовых всплеска." : `Файл: <strong>${escapeHTML(name)}</strong>. Проверьте состав данных перед загрузкой.`}</p><div class="import-summary">${[
      ["Товаров", data.products.length],
      ["Продаж", data.sales.length],
      ["Поставок", data.shipments.length],
    ]
      .map(
        ([label, count]) =>
          `<div><strong>${number(count)}</strong><span>${label}</span></div>`,
      )
      .join(
        "",
      )}</div>${data.source?.warnings?.length ? `<div class="notice import-warnings"><strong>Проверка источников</strong><ul>${data.source.warnings.map((warning) => `<li>${escapeHTML(warning)}</li>`).join("")}</ul></div>` : ""}<p class="notice">${state.snapshot?.data.products.length ? "Загрузка полностью заменит текущие данные склада. Сначала сохраните резервную копию, если хотите вернуться к ним." : "Данные будут сохранены в рабочее пространство. Рекомендации не отправляются поставщикам автоматически."}</p><div class="modal-actions">${xlsxImport ? '<button class="button secondary" data-action="back-import-files">Назад к файлам</button>' : ""}${state.snapshot?.revision ? '<button class="button secondary" data-action="backup">Скачать текущие данные</button>' : ""}<button class="button primary" id="confirm-import" data-action="confirm-import">${demo ? "Загрузить демоданные" : "Заменить данные"}</button></div>`,
    true,
  );
}

async function confirmImport() {
  if (state.busy || !state.pendingImport) return;
  const pending = state.pendingImport;
  busy(true);
  $("#confirm-import").disabled = true;
  try {
    const response = await api("dataset", {
      method: "PUT",
      headers: { "If-Match": pending.etag },
      body: JSON.stringify(pending.data),
    });
    state.snapshot = await response.json();
    state.etag = response.headers.get("ETag");
    state.result = null;
    if (pending.demo)
      state.params = {
        as_of: today(),
        lookback_days: 30,
        review_period_days: 14,
        safety_stock_days: 7,
      };
    if (pending.data.source?.as_of) {
      state.params.as_of = shiftDate(pending.data.source.as_of, 1);
      state.filter = "attention";
    }
    state.page = 1;
    state.pendingImport = null;
    state.search = "";
    state.supplier = "";
    $("#search").value = "";
    closeModal();
    clearError();
    await getRecommendations();
    toast("Данные сохранены, рекомендации готовы");
  } catch (error) {
    if (error.status === 412) {
      try {
        await getSnapshot();
        importPreview(pending.data, "Выбранные данные", pending.demo);
        modalError(
          new Error(
            "Другой пользователь изменил склад. Текущие данные обновлены. Проверьте их перед повторным подтверждением замены.",
          ),
        );
      } catch (refreshError) {
        modalError(refreshError);
      }
    } else if ($("#modal").open) modalError(error);
    else showError(error);
  } finally {
    render();
    busy(false);
    if ($("#confirm-import")) $("#confirm-import").disabled = false;
  }
}

function download(blob, filename) {
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.append(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

async function exportCSV() {
  if (state.busy || !state.result?.orders.length) return;
  busy(true);
  try {
    const response = await api("recommendations.csv", {
      method: "POST",
      body: JSON.stringify(state.result.parameters),
    });
    if (
      Number(response.headers.get("X-Dataset-Revision")) !==
      state.result.dataset_revision
    )
      throw new Error(
        "Данные изменились. Пересчитайте рекомендации перед экспортом.",
      );
    const csv = await response.text();
    download(
      new Blob(["\ufeff", csv], { type: "text/csv;charset=utf-8" }),
      `supplier-orders-${state.result.parameters.as_of}.csv`,
    );
    toast("Все рекомендации экспортированы в CSV");
  } catch (error) {
    showError(error);
  } finally {
    busy(false);
  }
}

document.addEventListener("click", async (event) => {
  const view = event.target.closest("[data-view]");
  if (view) {
    state.page = 1;
    state.view = view.dataset.view;
    state.filter = state.view === "inventory" ? "all" : "needed";
    $("#sidebar").classList.remove("open");
    $(".mobile-menu").setAttribute("aria-expanded", "false");
    syncSidebar();
    render();
    return;
  }
  const filter = event.target.closest("[data-filter]");
  if (filter) {
    state.page = 1;
    state.filter = filter.dataset.filter;
    render();
    return;
  }
  const detail = event.target.closest("[data-detail]");
  if (detail) {
    showDetail(detail.dataset.detail);
    return;
  }
  const removeFile = event.target.closest("[data-remove-import-file]");
  if (removeFile && xlsxImport && !state.busy) {
    rememberXlsxDates();
    xlsxImport.files.splice(Number(removeFile.dataset.removeImportFile), 1);
    xlsxImportForm();
    return;
  }
  const action = event.target.closest("[data-action]")?.dataset.action;
  if (!action) return;
  try {
    if (action === "menu") {
      const open = $("#sidebar").classList.toggle("open");
      $(".mobile-menu").setAttribute("aria-expanded", String(open));
      syncSidebar();
    }
    if (action === "previous-page") {
      state.page = Math.max(1, state.page - 1);
      renderTable();
    }
    if (action === "next-page") {
      state.page += 1;
      renderTable();
    }
    if (action === "close-modal") closeModal();
    if (action === "connect") connect();
    if (action === "settings") settings();
    if (action === "method") method();
    if (action === "refresh") await refresh();
    if (action === "calculate") await calculate();
    if (action === "export") await exportCSV();
    if (action === "confirm-import") await confirmImport();
    if (action === "add-import-files" && !state.busy) {
      rememberXlsxDates();
      $("#file-input").click();
    }
    if (action === "back-import-files" && !state.busy) xlsxImportForm();
    if (action === "import") {
      if (!state.snapshot) {
        connect();
        return;
      }
      resetImport();
      $("#file-input").click();
    }
    if (action === "demo" && state.snapshot)
      importPreview(demoDataset(), "Демонстрационные данные", true);
    if (action === "backup" && state.snapshot)
      download(
        new Blob([JSON.stringify(state.snapshot.data, null, 2)], {
          type: "application/json",
        }),
        `warehouse-backup-${today()}.json`,
      );
  } catch (error) {
    if ($("#modal").open) modalError(error);
    else showError(error);
  }
});

document.addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = event.target;
  if (form.id === "xlsx-import-form") await convertXlsx(form);
  if (form.id === "settings-form") {
    if (state.busy) return;
    const data = new FormData(form);
    const button = form.querySelector("button[type='submit']");
    button.disabled = true;
    busy(true);
    try {
      if (state.snapshot) {
        const updated = structuredClone(state.snapshot.data);
        let changed = false;
        updated.suppliers.forEach((supplier, index) => {
          const days = Number(data.get(`lead-${index}`));
          if (
            days !== supplier.lead_time_days ||
            supplier.lead_time_unconfirmed
          )
            changed = true;
          supplier.lead_time_days = days;
          supplier.lead_time_unconfirmed = false;
        });
        if (changed) {
          const response = await api("dataset", {
            method: "PUT",
            headers: { "If-Match": state.etag },
            body: JSON.stringify(updated),
          });
          state.snapshot = await response.json();
          state.etag = response.headers.get("ETag");
        }
      }
      state.params = {
        as_of: data.get("as_of"),
        lookback_days: Number(data.get("lookback_days")),
        review_period_days: Number(data.get("review_period_days")),
        safety_stock_days: Number(data.get("safety_stock_days")),
      };
      state.result = null;
      state.page = 1;
      closeModal();
      if (state.snapshot?.data.products.length) await getRecommendations();
      clearError();
    } catch (error) {
      if (error.status === 412)
        error.message =
          "Данные изменились. Закройте окно, обновите данные и повторите изменение сроков.";
      if ($("#modal").open) modalError(error);
      else showError(error);
    } finally {
      button.disabled = false;
      render();
      busy(false);
    }
  }
  if (form.id === "connect-form") {
    if (state.busy) return;
    const button = form.querySelector("button[type='submit']");
    button.disabled = true;
    const previousKey = state.key;
    state.key = new FormData(form).get("key").trim();
    busy(true);
    try {
      await getSnapshot();
      closeModal();
      clearError();
      if (state.snapshot.data.products.length) await getRecommendations();
      toast("Подключение установлено");
    } catch (error) {
      state.key = previousKey;
      if ($("#modal").open)
        modalError(
          new Error(
            error.status === 401
              ? "Ключ не подходит. Проверьте его и попробуйте снова."
              : error.message,
          ),
        );
      else showError(error);
    } finally {
      button.disabled = false;
      render();
      busy(false);
    }
  }
});

$("#file-input").addEventListener("change", async (event) => {
  const selected = [...event.target.files];
  event.target.value = "";
  if (!selected.length || state.busy) return;
  try {
    clearError();
    if (selected.some((file) => !/\.(json|xlsx)$/i.test(file.name)))
      throw new Error("Выберите файл JSON или отчёты Excel в формате .xlsx.");
    const jsonFiles = selected.filter((file) => /\.json$/i.test(file.name));
    if (jsonFiles.length && (selected.length !== 1 || xlsxImport))
      throw new Error(
        "JSON загружается одним файлом, отдельно от Excel. Закройте это окно и начните новую загрузку для выбора JSON.",
      );
    if (!jsonFiles.length) {
      const files = [...(xlsxImport?.files || [])];
      for (const file of selected) {
        const existing = files.find((item) => item.name === file.name);
        if (existing) {
          if (
            existing.size === file.size &&
            existing.lastModified === file.lastModified
          )
            continue;
          throw new Error(
            `Файл «${file.name}» уже добавлен. Удалите прежний файл перед заменой.`,
          );
        }
        files.push(file);
      }
      if (files.length > 12)
        throw new Error(
          "Можно загрузить до 12 файлов Excel: по шесть для каждого поставщика.",
        );
      if (files.reduce((sum, file) => sum + file.size, 0) > 64 * 1024 * 1024)
        throw new Error(
          "Общий размер файлов превышает 64 МБ. Сократите период истории в исходных отчётах.",
        );
      if (!xlsxImport) {
        resetImport();
        const asOf = shiftDate(state.params.as_of, -1);
        xlsxImport = {
          files,
          asOf,
          historyStart: `${String(Number(asOf.slice(0, 4)) - 1).padStart(4, "0")}-01-01`,
          converting: false,
        };
      } else {
        rememberXlsxDates();
        xlsxImport.files = files;
      }
      xlsxImportForm();
      return;
    }
    const file = jsonFiles[0];
    if (file.size > 64 * 1024 * 1024)
      throw new Error(
        "Размер файла превышает 64 МБ. Сократите период истории или число записей.",
      );
    resetImport();
    const request = importRequest;
    let data;
    try {
      data = JSON.parse(await file.text());
    } catch {
      if (request !== importRequest) return;
      throw new Error("Не удалось прочитать JSON. Проверьте формат файла.");
    }
    if (request !== importRequest) return;
    importPreview(
      data?.data && Array.isArray(data.data.products) ? data.data : data,
      file.name,
    );
  } catch (error) {
    if ($("#modal").open) modalError(error);
    else showError(error);
  }
});
document.addEventListener("input", (event) => {
  if (event.target.matches("#xlsx-import-form input[name='as_of']")) {
    $("#xlsx-import-form input[name='history_start']").max =
      event.target.value || "9990-12-31";
  }
});
$("#search").addEventListener("input", (event) => {
  state.page = 1;
  state.search = event.target.value.toLocaleLowerCase("ru-RU").trim();
  renderTable();
});
$("#supplier-filter").addEventListener("change", (event) => {
  state.page = 1;
  state.supplier = event.target.value;
  renderTable();
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape") {
    $("#sidebar").classList.remove("open");
    $(".mobile-menu").setAttribute("aria-expanded", "false");
    syncSidebar();
  }
});
$("#modal").addEventListener("close", () => {
  if (!$("#modal").open) resetImport();
});
let resizeFrame;
window.addEventListener("resize", () => {
  cancelAnimationFrame(resizeFrame);
  resizeFrame = requestAnimationFrame(renderChart);
});
syncSidebar();
render();
refresh();
