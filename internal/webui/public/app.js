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
const number = (value, digits = 0) =>
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
  insufficient_positive_days_for_spike_detection:
    "Мало дней с продажами для надёжной фильтрации всплесков",
  no_regular_demand: "В выбранном периоде нет регулярного спроса",
  overdue_shipments_excluded: "Просроченные поставки исключены из расчёта",
  insufficient_supply_during_lead_time:
    "Есть риск дефицита до поступления нового заказа",
};
let toastTimer;
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
  const headers = new Headers(options.headers);
  if (state.key) headers.set("Authorization", `Bearer ${state.key}`);
  if (options.body) headers.set("Content-Type", "application/json");
  let response;
  try {
    response = await fetch(`/api/v1/${path}`, {
      ...options,
      headers,
      signal: AbortSignal.timeout(30000),
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
  const size = result.parameters.lookback_days;
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
  const width = Math.max(280, $("#demand-chart").clientWidth - 36),
    height = 165,
    left = 40,
    right = 10,
    bottom = 26,
    yTop = 8;
  const x = (i) => left + (i / Math.max(1, size - 1)) * (width - left - right);
  const y = (v) => height - bottom - (v / top) * (height - bottom - yTop);
  const path = (key) =>
    points
      .map(
        (p, i) => `${i ? "L" : "M"}${x(i).toFixed(2)},${y(p[key]).toFixed(2)}`,
      )
      .join(" ");
  const grid = Array.from({ length: 5 }, (_, i) => {
    const value = (top * i) / 4;
    return `<line class="grid" x1="${left}" y1="${y(value)}" x2="${width - right}" y2="${y(value)}"/><text x="${left - 10}" y="${y(value) + 3}" text-anchor="end">${number(value)}</text>`;
  }).join("");
  const labels = Array.from(
    new Set(
      Array.from({ length: Math.min(6, size) }, (_, i) =>
        Math.round((i * (size - 1)) / (Math.min(6, size) - 1)),
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
      `${line.name} ${line.sku}`
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
  if (!lines.length) {
    $("#table-content").innerHTML = emptyTable();
    return;
  }
  $("#table-content").innerHTML =
    `<div class="table-scroll"><table><thead><tr><th scope="col">Товар / Артикул</th><th scope="col">Поставщик</th><th scope="col">Спрос / день</th><th scope="col">Доступно</th><th scope="col">В пути</th><th scope="col">К закупке</th><th scope="col">Статус</th><th scope="col"><span class="muted">Расчёт</span></th></tr></thead><tbody>${lines
      .map((line) => {
        const risk = line.warnings.includes(
          "insufficient_supply_during_lead_time",
        );
        const adjusted = line.adjustments.length > 0;
        const status = risk
          ? ["orange", "Риск дефицита"]
          : adjusted
            ? ["purple", "Спрос скорректирован"]
            : line.order_quantity > 0
              ? ["purple", "К закупке"]
              : ["green", "Запас в норме"];
        return `<tr><td><div class="product-cell"><span class="product-icon">${icon("box")}</span><div><div class="product-name">${escapeHTML(line.name)}</div><div class="product-sku">${escapeHTML(line.sku)}</div></div></div></td><td>${escapeHTML(supplierMap.get(line.supplier_id))}</td><td class="numeric">${number(line.daily_demand, 2)}</td><td class="numeric">${number(line.available_stock)}</td><td class="numeric">${number(line.incoming_quantity)}</td><td class="order-quantity numeric">${line.order_quantity ? number(line.order_quantity) : "—"}</td><td><span class="badge ${status[0]}">${status[1]}</span></td><td><button class="row-detail" data-detail="${escapeHTML(line.product_id)}" aria-label="Расчёт для ${escapeHTML(line.name)}">${icon("chevron")}</button></td></tr>`;
      })
      .join("")}</tbody></table></div>`;
}

function renderShipments(supplierMap) {
  const products = new Map(
    (state.snapshot?.data.products || []).map((p) => [p.id, p]),
  );
  const shipments = (state.snapshot?.data.shipments || [])
    .filter((s) => {
      const p = products.get(s.product_id);
      return (
        p &&
        (!state.supplier || p.supplier_id === state.supplier) &&
        `${p.name} ${p.sku}`.toLocaleLowerCase("ru-RU").includes(state.search)
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

function openModal(title, body) {
  $("#modal-title").textContent = title;
  $("#modal-body").innerHTML = body;
  if (!$("#modal").open) $("#modal").showModal();
}
function closeModal() {
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
}

function settings() {
  openModal(
    "Параметры расчёта",
    `<p class="modal-copy">Настройте период анализа и запас. Продажи за дату расчёта не учитываются, поскольку день ещё может быть неполным.</p><form id="settings-form"><div class="form-grid"><label class="field full">Дата расчёта<input name="as_of" type="date" value="${state.params.as_of}" min="1900-01-01" max="9990-12-31" required></label><label class="field">История продаж, дней<input name="lookback_days" type="number" min="7" max="730" step="1" value="${state.params.lookback_days}" required></label><label class="field">Период закупки, дней<input name="review_period_days" type="number" min="1" max="365" step="1" value="${state.params.review_period_days}" required></label><label class="field full">Страховой запас, дней<input name="safety_stock_days" type="number" min="0" max="365" step="1" value="${state.params.safety_stock_days}" required><small>Добавляется к сроку поставки и периоду закупки.</small></label></div><div class="modal-actions"><button class="button secondary" type="button" data-action="close-modal">Отмена</button><button class="button primary" type="submit">Применить и рассчитать</button></div></form>`,
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
    `<p class="modal-copy">Прозрачная рекомендация на основе данных вашего склада.</p><ol class="method-list"><li><strong>Находим регулярный спрос.</strong> Анализируем полные календарные дни. Пропущенные дни считаются днями без продаж.</li><li><strong>Убираем разовые всплески.</strong> Сравниваем продажи с медианой и разбросом. При недостатке истории оставляем продажи и показываем предупреждение.</li><li><strong>Учитываем доступный запас.</strong> Вычитаем резервы, добавляем поставки в пределах горизонта. Просроченные поставки исключаем.</li><li><strong>Рассчитываем закупку.</strong> Покрываем срок поставки, период закупки и страховой запас. Округляем заказ до упаковки и минимальной партии.</li></ol><p class="notice">График суммирует базовые единицы разных товаров. Рекомендации рассчитываются отдельно по каждой позиции. Сезонность и временное отсутствие товара не моделируются.</p><div class="modal-actions"><button class="button primary" data-action="close-modal">Понятно</button></div>`,
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
    `<p class="modal-copy">${escapeHTML(line.sku)} · Покрытие до ${date(line.coverage_end)}</p><div class="detail-grid">${pairs.map(([label, value]) => `<div class="detail-item"><span>${label}</span><strong>${value}</strong></div>`).join("")}</div><div class="detail-total"><span>Рекомендовано к закупке</span><strong>${number(line.order_quantity)}</strong></div>${line.adjustments.length ? `<h3 class="detail-subtitle">Корректировки продаж</h3>${line.adjustments.map((a) => `<div class="adjustment-row"><span>${date(a.date)} · ${a.reason === "sales_spike" ? "Всплеск" : "Ручное исключение"}</span><strong>${number(a.original_quantity)} → ${number(a.used_quantity, 1)}</strong></div>`).join("")}` : ""}${line.warnings.length ? `<h3 class="detail-subtitle">Обратите внимание</h3><ul class="warning-list">${line.warnings.map((w) => `<li>${escapeHTML(warningLabels[w] || w)}</li>`).join("")}</ul>` : ""}<div class="modal-actions"><button class="button primary" data-action="close-modal">Готово</button></div>`,
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
      )}</div><p class="notice">${state.snapshot?.data.products.length ? "Загрузка полностью заменит текущие данные склада. Сначала сохраните резервную копию, если хотите вернуться к ним." : "Данные будут сохранены в рабочее пространство. Рекомендации не отправляются поставщикам автоматически."}</p><div class="modal-actions">${state.snapshot?.revision ? '<button class="button secondary" data-action="backup">Скачать текущие данные</button>' : ""}<button class="button primary" id="confirm-import" data-action="confirm-import">${demo ? "Загрузить демоданные" : "Заменить данные"}</button></div>`,
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
    state.filter = filter.dataset.filter;
    render();
    return;
  }
  const detail = event.target.closest("[data-detail]");
  if (detail) {
    showDetail(detail.dataset.detail);
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
    if (action === "close-modal") closeModal();
    if (action === "connect") connect();
    if (action === "settings") settings();
    if (action === "method") method();
    if (action === "refresh") await refresh();
    if (action === "calculate") await calculate();
    if (action === "export") await exportCSV();
    if (action === "confirm-import") await confirmImport();
    if (action === "import") {
      if (!state.snapshot) {
        connect();
        return;
      }
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
  if (form.id === "settings-form") {
    const data = new FormData(form);
    state.params = {
      as_of: data.get("as_of"),
      lookback_days: Number(data.get("lookback_days")),
      review_period_days: Number(data.get("review_period_days")),
      safety_stock_days: Number(data.get("safety_stock_days")),
    };
    state.result = null;
    closeModal();
    render();
    await calculate();
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
  const file = event.target.files[0];
  event.target.value = "";
  if (!file) return;
  try {
    if (file.size > 8 * 1024 * 1024)
      throw new Error(
        "Размер файла превышает 8 МБ. Сократите период истории или число записей.",
      );
    let data;
    try {
      data = JSON.parse(await file.text());
    } catch {
      throw new Error("Не удалось прочитать JSON. Проверьте формат файла.");
    }
    importPreview(
      data.data && Array.isArray(data.data.products) ? data.data : data,
      file.name,
    );
  } catch (error) {
    showError(error);
  }
});
$("#search").addEventListener("input", (event) => {
  state.search = event.target.value.toLocaleLowerCase("ru-RU").trim();
  renderTable();
});
$("#supplier-filter").addEventListener("change", (event) => {
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
  state.pendingImport = null;
});
let resizeFrame;
window.addEventListener("resize", () => {
  cancelAnimationFrame(resizeFrame);
  resizeFrame = requestAnimationFrame(renderChart);
});
syncSidebar();
render();
refresh();
