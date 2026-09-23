import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, mkdir, readFile, readdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { basename, join, resolve } from "node:path";
import test from "node:test";
import { chromium } from "playwright";

// Runs against its own backend process and temporary warehouse, never user data.
test("purchasing dashboard end-to-end", { timeout: 180000 }, async (t) => {
  const dir = await mkdtemp(join(tmpdir(), "warehouse-ui-test-"));
  const key = "browser-test-key-at-least-24-characters";
  const server = spawn(resolve("bin/backend"), [], {
    env: {
      ...process.env,
      HTTP_ADDR: "127.0.0.1:0",
      DATA_FILE: join(dir, "warehouse.json"),
      API_KEY: key,
    },
    stdio: ["ignore", "pipe", "pipe"],
  });
  let browser;
  t.after(async () => {
    await browser?.close();
    if (server.exitCode === null && server.pid) {
      const ended = once(server, "exit");
      server.kill("SIGTERM");
      await ended;
    }
    await rm(dir, { recursive: true, force: true });
  });
  const address = await new Promise((resolveAddress, reject) => {
    const timer = setTimeout(
      () => reject(new Error("backend startup timed out")),
      10000,
    );
    server.on("error", (error) => {
      clearTimeout(timer);
      reject(error);
    });
    server.stdout.on("data", (chunk) => {
      const match = chunk.toString().match(/"address":"([^"]+)"/);
      if (match) {
        clearTimeout(timer);
        resolveAddress(match[1]);
      }
    });
    server.on("exit", (code) => {
      clearTimeout(timer);
      reject(new Error(`backend exited: ${code}`));
    });
  });
  const base = `http://${address}`;
  const api = async (path, options = {}) =>
    fetch(`${base}/api/v1/${path}`, {
      ...options,
      headers: {
        Authorization: `Bearer ${key}`,
        "Content-Type": "application/json",
        ...options.headers,
      },
    });
  browser = await chromium.launch({
    executablePath: process.env.CHROME_BIN || undefined,
    headless: true,
    args: ["--no-sandbox", "--disable-dev-shm-usage"],
  });
  const page = await browser.newPage({
    viewport: { width: 1440, height: 1100 },
    reducedMotion: "reduce",
  });
  page.setDefaultTimeout(10000);
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (message) => {
    if (
      message.type() === "error" &&
      /Content Security Policy|Refused to/.test(message.text())
    )
      errors.push(message.text());
  });
  const loaded = async () =>
    page.locator("#loading").waitFor({ state: "hidden" });
  const snapshot = async () => {
    const res = await api("dataset");
    return { data: await res.json(), etag: res.headers.get("etag") };
  };
  const tableGeometry = () => page.locator("#table-content").evaluate((root) => {
    const table = root.querySelector("table");
    const scroll = root.querySelector(".table-scroll");
    return {
      width: table.getBoundingClientRect().width,
      scrollWidth: scroll.scrollWidth,
      clientWidth: scroll.clientWidth,
      rows: [...table.querySelectorAll("tbody tr:not(.explanation-row)")].map(
        (row) => [...row.cells].map((cell) => {
          const rect = cell.getBoundingClientRect();
          return [rect.width, rect.height];
        }),
      ),
    };
  });
  const checkExplanation = async (summary, expected, key) => {
    const before = await tableGeometry();
    if (key) {
      await summary.focus();
      await summary.press(key);
    } else {
      await summary.click();
    }
    const id = await summary.getAttribute("aria-controls");
    const row = page.locator(`#${id}`);
    await row.waitFor({ state: "visible" });
    assert.equal(await row.locator("p").textContent(), expected);
    assert.equal(await row.locator("td").getAttribute("colspan"), "8");
    assert.deepEqual(await tableGeometry(), before, "expansion must preserve table width and ordinary rows");
    const layout = await row.evaluate((element) => {
      const text = element.querySelector("p");
      const range = document.createRange();
      range.selectNodeContents(text);
      return {
        width: element.getBoundingClientRect().width,
        lines: new Set([...range.getClientRects()].map((rect) => rect.top)).size,
        textOverflow: text.scrollWidth > text.clientWidth,
        pageOverflow: document.documentElement.scrollWidth > window.innerWidth,
      };
    });
    assert.equal(layout.width, before.width, "explanation must span the whole table");
    assert.ok(layout.lines > 1, "explanation must wrap across multiple lines");
    assert.equal(layout.textOverflow, false, "explanation must not overflow its cell");
    assert.equal(layout.pageOverflow, false, "explanation must not overflow the page");
    assert.equal(await row.locator("img").count(), 0, "explanation remains plain text");
    await summary.press("Space");
    await row.waitFor({ state: "detached" });
    assert.deepEqual(await tableGeometry(), before, "collapse restores the table");
  };
  const supplierWorkbooks = async () => {
    const root = resolve(process.env.SUPPLIER_WORKBOOK_DIR);
    const workbookPaths = async (directory) =>
      (await readdir(directory))
        .filter((name) => name.endsWith(".xlsx") && !name.startsWith("~$"))
        .sort()
        .map((name) => join(directory, name));
    const iek = await workbookPaths(join(root, "IEK"));
    const systeme = await workbookPaths(join(root, "systemElectric"));
    // Prefer the separate MOQ when present to exercise multi-directory selection.
    const rootMOQ = join(root, "MOQ  ИЭК.xlsx");
    const moq = (await workbookPaths(root)).includes(rootMOQ)
      ? rootMOQ
      : join(root, "IEK", "MOQ  ИЭК.xlsx");
    const iekReports = iek.filter((path) => basename(path) !== "MOQ  ИЭК.xlsx");
    assert.equal(iekReports.length + 1, 6);
    assert.equal(systeme.length, 6);
    return { iek: [...iekReports, moq], systeme };
  };
  const selectWorkbooks = async (files) => {
    await page.locator("#file-input").setInputFiles(files);
    await page.locator("#xlsx-import-form").waitFor();
    assert.equal(
      await page.locator(".import-files-heading strong").innerText(),
      `Выбрано файлов: ${files.length}`,
    );
    await page.locator("#xlsx-import-form [name='as_of']").fill("2026-09-22");
    await page.locator("#xlsx-import-form [name='history_start']").fill("2025-01-01");
  };

  await t.test(
    "public shell, protected data, login and empty state",
    async () => {
      await page.goto(base);
      await page.getByRole("button", { name: "Настроить подключение" }).click();
      await page.getByLabel("Ключ доступа", { exact: true }).fill("wrong-key");
      await page
        .locator("#connect-form")
        .getByRole("button", { name: "Подключиться", exact: true })
        .click();
      await page.getByText("Ключ не подходит.", { exact: false }).waitFor();
      await page.getByLabel("Ключ доступа", { exact: true }).fill(key);
      await page
        .locator("#connect-form")
        .getByRole("button", { name: "Подключиться", exact: true })
        .click();
      await page
        .getByRole("heading", {
          name: "Хороший план начинается с ваших данных",
        })
        .waitFor();
      assert.equal(
        await page.evaluate(() => localStorage.length + sessionStorage.length),
        0,
      );
      assert.equal((await fetch(`${base}/api/v1/dataset`)).status, 401);
    },
  );

  await t.test("sidebar profile is static and the connection control still works", async () => {
    const profile = page.locator(".account");
    await profile.getByText("Электрокомплект", { exact: true }).click();
    assert.equal(await page.locator("#modal").isVisible(), false);
    assert.equal(await profile.evaluate((element) => element.tabIndex), -1);
    assert.equal(await profile.evaluate((element) => getComputedStyle(element).cursor), "auto");
    await page.locator(".sidebar-tip button").focus();
    await page.keyboard.press("Tab");
    assert.equal(await page.locator(".connection").evaluate((element) => element === document.activeElement), true);
    await page.locator(".connection").press("Enter");
    await page.getByRole("heading", { name: "Подключение к складу" }).waitFor();
    await page.locator("#modal").getByRole("button", { name: "Закрыть", exact: true }).click();
  });

  await t.test(
    "explicit demo import, real demand calculations and charts",
    async () => {
      await page.getByRole("button", { name: "Попробовать демо" }).click();
      assert.equal(
        (await snapshot()).data.revision,
        0,
        "preview must not write data",
      );
      await page.getByRole("button", { name: "Загрузить демоданные" }).click();
      await page.locator("table tbody tr").first().waitFor();
      await loaded();
      assert.equal((await snapshot()).data.revision, 1);
      assert.equal(
        await page
          .locator(".stat-card")
          .nth(2)
          .locator(".stat-number")
          .innerText(),
        "3корректировок",
      );
      assert.equal(await page.locator("#demand-chart svg").count(), 1);
      assert.equal(await page.locator("table tbody tr").count(), 5);
      const screenshotDir = process.env.UI_SCREENSHOT_DIR;
      if (screenshotDir) {
        await mkdir(screenshotDir, { recursive: true });
        await page.locator("#toast").waitFor({ state: "hidden" });
        await page.evaluate(() => window.scrollTo(0, 0));
        await page.screenshot({
          path: join(screenshotDir, "dashboard-desktop.png"),
          fullPage: true,
        });
      }
    },
  );

  await t.test("inline explanations wrap without adding horizontal overflow", async () => {
    const initial = await snapshot();
    const prose = "Регулярный спрос рассчитан по завершённым периодам с учётом запасов и поставок. ".repeat(30);
    const unbroken = `Артикул-${"АБ123".repeat(800)} <img src=x onerror="alert(1)">`;
    const url = "**/api/v1/recommendations";
    let fixture;
    await page.route(url, async (route) => {
      const response = await route.fetch();
      fixture = await response.json();
      for (const line of fixture.products) {
        line.explanation = line.product_id === "cable" ? prose : unbroken;
      }
      await route.fulfill({ response, json: fixture });
    });
    try {
      await page.locator("#calculate-button").click();
      await loaded();
      const summaries = page.locator("#table-content summary");
      const cable = page.locator("details[data-explanation='cable'] summary");
      const other = page.locator("details[data-explanation]:not([data-explanation='cable']) summary").first();
      for (const width of [1920, 1440, 390]) {
        await page.setViewportSize({ width, height: 1100 });
        const geometry = await tableGeometry();
        if (width === 1920) assert.equal(geometry.scrollWidth, geometry.clientWidth, "desktop table fits without a scrollbar");
        await checkExplanation(cable, prose);
        await checkExplanation(other, unbroken, "Enter");
      }
      await page.setViewportSize({ width: 1440, height: 1100 });
      await summaries.nth(0).click();
      await page.locator(".explanation-row").waitFor();
      await summaries.nth(1).click();
      await page.waitForFunction(() => document.querySelectorAll(".explanation-row").length === 2);
      await summaries.nth(0).click();
      await page.waitForFunction(() => document.querySelectorAll(".explanation-row").length === 1);
      await page.getByLabel("Поиск товаров").fill("ВВГ");
      assert.equal(await page.locator(".explanation-row").count(), 0, "filtering removes stale explanations");
      assert.equal(await page.locator("#table-content details[open]").count(), 0);
      await checkExplanation(cable, prose);
      assert.deepEqual(errors, [], "browser errors or CSP violations");
    } finally {
      await page.setViewportSize({ width: 1440, height: 1100 });
      await page.unroute(url);
      await page.getByLabel("Поиск товаров").fill("");
      await page.locator("#calculate-button").click();
      await loaded();
    }
    assert.deepEqual(await snapshot(), initial, "expanding explanations must not change warehouse data");
  });

  await t.test(
    "search, supplier filter, details, settings and CSV",
    async () => {
      await page.getByLabel("Поиск товаров").fill("ВВГ");
      assert.equal(await page.locator("table tbody tr").count(), 1);
      await page.locator("[data-detail='cable']").click();
      await page
        .getByRole("heading", { name: "Корректировки продаж" })
        .waitFor();
      await page.getByRole("button", { name: "Готово", exact: true }).click();
      await page.getByLabel("Поиск товаров").fill("");
      await page.getByLabel("Поставщик", { exact: true }).selectOption("light");
      assert.equal(await page.locator("table tbody tr").count(), 2);
      await page.getByLabel("Поставщик", { exact: true }).selectOption("");
      await page.getByRole("button", { name: "Параметры расчёта" }).click();
      await page.getByLabel("Период закупки, дней").fill("7");
      await page
        .getByRole("button", { name: "Применить и рассчитать" })
        .click();
      await loaded();
      const downloadPromise = page.waitForEvent("download");
      await page.getByRole("button", { name: "Экспорт CSV" }).click();
      const download = await downloadPromise;
      const csv = (await readFile(await download.path())).toString("utf8");
      assert.ok(csv.startsWith("\ufeffsupplier_id,"));
      assert.ok(csv.includes("Кабель силовой"));
      await loaded();
    },
  );

  await t.test(
    "all products in recommendations, shipments, mobile navigation and no overflow",
    async () => {
      await page.locator("[data-view='orders']").click();
      await page.getByRole("button", { name: "Все товары", exact: true }).click();
      assert.equal(await page.locator("table tbody tr").count(), 6);
      await page
        .getByRole("button", { name: "Товары в пути", exact: true })
        .click();
      assert.equal(await page.locator("table tbody tr").count(), 3);
      await page.getByText("Просрочена", { exact: true }).waitFor();
      await page.setViewportSize({ width: 390, height: 844 });
      await page.getByRole("button", { name: "Открыть меню" }).click();
      await page
        .getByRole("button", { name: "Обзор закупок", exact: true })
        .click();
      assert.ok(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= window.innerWidth,
        ),
      );
      if (process.env.UI_SCREENSHOT_DIR) {
        await page.locator("#toast").waitFor({ state: "hidden" });
        await page.evaluate(() => window.scrollTo(0, 0));
        await page.screenshot({
          path: join(process.env.UI_SCREENSHOT_DIR, "dashboard-mobile.png"),
          fullPage: true,
        });
      }
      await page.setViewportSize({ width: 1440, height: 1100 });
    },
  );

  await t.test(
    "invalid import, conflict protection and HTML escaping",
    async () => {
      await page.locator("#file-input").setInputFiles({
        name: "invalid.json",
        mimeType: "application/json",
        buffer: Buffer.from("{}"),
      });
      await page
        .getByRole("alert")
        .filter({ hasText: "Файл должен содержать" })
        .waitFor();
      const initial = await snapshot();
      const candidate = structuredClone(initial.data.data);
      candidate.products.find((p) => p.id === "cable").name =
        '<img src=x onerror="alert(1)">';
      await page.locator("#file-input").setInputFiles({
        name: "warehouse.json",
        mimeType: "application/json",
        buffer: Buffer.from(JSON.stringify(candidate)),
      });
      const changed = await api("dataset", {
        method: "PUT",
        headers: { "If-Match": initial.etag },
        body: JSON.stringify(initial.data.data),
      });
      assert.equal(changed.status, 200);
      await page
        .getByRole("button", { name: "Заменить данные", exact: true })
        .click();
      await page
        .getByText("Другой пользователь изменил склад.", { exact: false })
        .waitFor();
      assert.equal(
        (await snapshot()).data.revision,
        2,
        "conflicting import must not overwrite",
      );
      await loaded();
      await page
        .getByRole("button", { name: "Заменить данные", exact: true })
        .click();
      await page.locator("#modal").waitFor({ state: "hidden" });
      await loaded();
      await page
        .getByText('<img src=x onerror="alert(1)">', { exact: true })
        .waitFor();
      assert.equal(await page.locator("img").count(), 0);
      assert.equal((await snapshot()).data.revision, 3);
      assert.deepEqual(errors, [], "browser errors or CSP violations");
    },
  );
  await t.test(
    "Excel selection rejects mixed formats and invalid workbooks without saving",
    async () => {
      const initial = await snapshot();
      const invalidWorkbook = {
        name: "unsupported.xlsx",
        mimeType:
          "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
        buffer: Buffer.from("This is not an Excel workbook."),
      };
      const input = page.locator("#file-input");
      assert.match(await input.getAttribute("accept"), /\.json/);
      assert.match(await input.getAttribute("accept"), /\.xlsx/);
      assert.equal(await input.getAttribute("multiple"), "");
      await input.setInputFiles({
        name: "legacy.xls",
        mimeType: "application/vnd.ms-excel",
        buffer: Buffer.from([0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1]),
      });
      await page.locator("#error-banner").waitFor({ state: "visible" });
      assert.match(await page.locator("#error-banner").innerText(), /формате \.xlsx/);
      assert.equal(await page.locator("#modal").isVisible(), false);
      assert.deepEqual(await snapshot(), initial, "unsupported XLS must not save");
      await input.setInputFiles([
        {
          name: "warehouse.json",
          mimeType: "application/json",
          buffer: Buffer.from(JSON.stringify(initial.data.data)),
        },
        invalidWorkbook,
      ]);
      await page.locator("#error-banner").waitFor({ state: "visible" });
      assert.equal(await page.locator("#modal").isVisible(), false);
      assert.equal((await snapshot()).data.revision, initial.data.revision);

      await input.setInputFiles(invalidWorkbook);
      await page.locator("#xlsx-import-form").waitFor();
      await page.locator("#xlsx-import-form [name='as_of']").fill("2026-09-22");
      await page
        .locator("#xlsx-import-form [name='history_start']")
        .fill("2025-01-01");
      await page.locator("#convert-xlsx").click();
      await page.locator("#modal-error").waitFor({ state: "visible" });
      assert.match(await page.locator("#modal-error").innerText(), /[А-Яа-яЁё]/);
      assert.doesNotMatch(
        await page.locator("#modal-error").innerText(),
        /unrecognized supplier|unsupported workbook|missing required workbooks/i,
      );
      assert.equal(await page.locator("#confirm-import").count(), 0);
      assert.deepEqual(
        await snapshot(),
        initial,
        "failed conversion must not save",
      );
      await page
        .locator("#modal")
        .getByRole("button", { name: "Закрыть", exact: true })
        .click();
    },
  );
  await t.test(
    "cancelled Excel conversion cannot replace a newer file selection",
    async () => {
      const initial = await snapshot();
      let reply;
      let replied;
      const releaseResponse = new Promise((resolveResponse) => {
        reply = resolveResponse;
      });
      const responseFinished = new Promise((resolveResponse) => {
        replied = resolveResponse;
      });
      const url = "**/api/v1/import/xlsx";
      await page.route(url, async (route) => {
        await releaseResponse;
        try {
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({ data: initial.data.data }),
          });
        } finally {
          replied();
        }
      });
      try {
        const workbook = (name) => ({
          name,
          mimeType:
            "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
          buffer: Buffer.from(
            "Conversion response is controlled by this test.",
          ),
        });
        await page.locator("#file-input").setInputFiles(workbook("first.xlsx"));
        await page.locator("#xlsx-import-form").waitFor();
        const request = page.waitForRequest("**/api/v1/import/xlsx");
        await page.locator("#convert-xlsx").click();
        await request;
        await page
          .locator("#modal")
          .getByRole("button", { name: "Закрыть", exact: true })
          .click();
        await page
          .locator("#file-input")
          .setInputFiles(workbook("second.xlsx"));
        await page.locator("#xlsx-import-form").waitFor();
        reply();
        await responseFinished;
        assert.equal(await page.locator("#confirm-import").count(), 0);
        assert.equal(
          await page.locator("#xlsx-file-list strong").innerText(),
          "second.xlsx",
        );
        assert.equal(await page.locator("#convert-xlsx").isEnabled(), true);
        assert.deepEqual(await snapshot(), initial);
        await page
          .locator("#modal")
          .getByRole("button", { name: "Закрыть", exact: true })
          .click();
      } finally {
        reply();
        await page.unroute(url);
      }
    },
  );
  for (const scenario of [
    { supplier: "iek", name: "IEK" },
    { supplier: "systeme", name: "Systeme Electric" },
    { supplier: "systeme", name: "Systeme Electric with a generic daily-sales filename", genericDailyName: true },
  ]) {
    await t.test(
      `complete ${scenario.name} Excel set is accepted without the other supplier`,
      { skip: !process.env.SUPPLIER_WORKBOOK_DIR, timeout: 90000 },
      async () => {
        const initial = await snapshot();
        const batches = await supplierWorkbooks();
        let files = batches[scenario.supplier];
        if (scenario.genericDailyName) {
          files = await Promise.all(files.map(async (path) => ({
            name: basename(path).startsWith("Динамика продаж")
              ? "Динамика продаж_2025-2026.xlsx"
              : basename(path),
            mimeType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
            buffer: await readFile(path),
          })));
        }
        let attemptedSave = false;
        try {
          await selectWorkbooks(files);
          const responsePromise = page.waitForResponse("**/api/v1/import/xlsx", { timeout: 90000 });
          await page.locator("#convert-xlsx").click();
          const response = await responsePromise;
          assert.equal(response.status(), 200);
          await page.locator("#confirm-import").waitFor();
          assert.deepEqual(await snapshot(), initial, "supplier preview must not save");
          attemptedSave = true;
          await page.locator("#confirm-import").click();
          await page.locator("#modal").waitFor({ state: "hidden", timeout: 30000 });
          await loaded();
          // Read the saved data directly; large Excel responses can exceed Chrome's inspector cache.
          const imported = await snapshot();
          assert.equal(imported.data.revision, initial.data.revision + 1);
          assert.deepEqual(imported.data.data.suppliers.map((supplier) => supplier.id), [scenario.supplier]);
          assert.ok(imported.data.data.products.length > 0);
          assert.ok(imported.data.data.products.every((product) => product.supplier_id === scenario.supplier));
        } finally {
          if (await page.locator("#modal").isVisible()) {
            await page.locator("#modal").getByRole("button", { name: "Закрыть", exact: true }).click();
          }
          if (attemptedSave) {
            await loaded();
            await page.locator("#file-input").setInputFiles({
              name: "warehouse-backup.json",
              mimeType: "application/json",
              buffer: Buffer.from(JSON.stringify(initial.data)),
            });
            await page.locator("#confirm-import").waitFor();
            await page.locator("#confirm-import").click();
            await page.locator("#modal").waitFor({ state: "hidden" });
            await loaded();
            assert.deepEqual((await snapshot()).data.data, initial.data.data);
          }
        }
      },
    );
  }
  await t.test(
    "incomplete Excel sets list missing files in Russian only for affected suppliers",
    { skip: !process.env.SUPPLIER_WORKBOOK_DIR, timeout: 90000 },
    async () => {
      const initial = await snapshot();
      const batches = await supplierWorkbooks();
      const incomplete = (files) => files.filter((path) => !basename(path).startsWith("Сезонность"));
      for (const scenario of [
        { files: incomplete(batches.iek), affected: ["IEK"], absent: /Systeme Electric/ },
        { files: incomplete(batches.systeme), affected: ["Systeme Electric"], absent: /IEK|ИЭК/ },
        { files: [...incomplete(batches.iek), ...incomplete(batches.systeme)], affected: ["IEK", "Systeme Electric"] },
        { files: [...batches.iek, ...incomplete(batches.systeme)], affected: ["Systeme Electric"], absent: /IEK|ИЭК/ },
      ]) {
        try {
          await selectWorkbooks(scenario.files);
          await page.locator("#convert-xlsx").click();
          await page.locator("#modal-error").waitFor({ state: "visible", timeout: 90000 });
          const message = await page.locator("#modal-error").innerText();
          for (const supplier of scenario.affected) {
            assert.ok(message.includes(`${supplier}: не хватает файлов: Сезонность`), message);
          }
          if (scenario.absent) assert.doesNotMatch(message, scenario.absent);
          assert.doesNotMatch(message, /missing required workbooks|select all six|monthly_sales|transactions/i);
          assert.equal(await page.locator("#confirm-import").count(), 0);
          assert.deepEqual(await snapshot(), initial, "incomplete supplier set must not save");
        } finally {
          if (await page.locator("#modal").isVisible()) {
            await page.locator("#modal").getByRole("button", { name: "Закрыть", exact: true }).click();
          }
        }
      }
    },
  );
  await t.test(
    "original Excel batches accumulate, preview safely and restore a JSON backup",
    { skip: !process.env.SUPPLIER_WORKBOOK_DIR, timeout: 120000 },
    async () => {
      const batches = await supplierWorkbooks();
      const iek = batches.iek.slice(0, -1);
      const moq = batches.iek.at(-1);
      const systeme = batches.systeme;
      assert.equal(iek.length + systeme.length + 1, 12);
      const initial = await snapshot();
      const input = page.locator("#file-input");
      const removeButtons = page.locator(
        "#xlsx-file-list [data-remove-import-file]",
      );

      await input.setInputFiles(iek);
      await page.locator("#xlsx-import-form").waitFor();
      assert.equal(await removeButtons.count(), iek.length);
      const chooser = page.waitForEvent("filechooser");
      await page.locator("[data-action='add-import-files']").click();
      await (await chooser).setFiles(systeme);
      assert.equal(await removeButtons.count(), iek.length + systeme.length);
      await input.setInputFiles(moq);
      assert.equal(await removeButtons.count(), 12);
      await removeButtons.first().click();
      assert.equal(await removeButtons.count(), 11);
      await input.setInputFiles(iek[0]);
      assert.equal(await removeButtons.count(), 12);
      assert.equal(await page.locator(".import-files-heading strong").innerText(), "Выбрано файлов: 12");
      await page.locator("#xlsx-import-form [name='as_of']").fill("2026-09-22");
      await page
        .locator("#xlsx-import-form [name='history_start']")
        .fill("2025-01-01");
      await page.locator("#convert-xlsx").click();
      await page.locator("#confirm-import").waitFor({ timeout: 90000 });
      assert.deepEqual(
        await snapshot(),
        initial,
        "Excel preview must not save",
      );
      const counts = await page
        .locator("#modal .import-summary strong")
        .allTextContents();
      assert.deepEqual(
        counts.map((value) => Number(value.replace(/\D/g, ""))),
        [3909, 140922, 313],
      );
      await page.locator("#confirm-import").click();
      await page.locator("#modal").waitFor({ state: "hidden", timeout: 30000 });
      await loaded();
      const imported = await snapshot();
      assert.equal(imported.data.revision, initial.data.revision + 1);
      assert.deepEqual(imported.data.data.suppliers.map((supplier) => supplier.id).sort(), ["iek", "systeme"]);
      assert.equal(imported.data.data.products.length, 3909);
      assert.equal(imported.data.data.sales.length, 140922);
      assert.equal(imported.data.data.shipments.length, 313);
      await page
        .locator("#source-notice")
        .getByText("IEK / Systeme Electric · Алматы", { exact: true })
        .waitFor();
      assert.equal(await page.locator("table tbody tr").count(), 100);
      assert.equal(await page.locator("#export-button").isDisabled(), true);

      await input.setInputFiles({
        name: "warehouse-backup.json",
        mimeType: "application/json",
        buffer: Buffer.from(JSON.stringify(initial.data)),
      });
      await page.locator("#confirm-import").waitFor();
      assert.equal((await snapshot()).data.revision, imported.data.revision);
      await page.locator("#confirm-import").click();
      await page.locator("#modal").waitFor({ state: "hidden" });
      await loaded();
      const restored = await snapshot();
      assert.deepEqual(restored.data.data, initial.data.data);
      assert.equal(restored.data.revision, imported.data.revision + 1);
      assert.deepEqual(errors, [], "browser errors or CSP violations");
    },
  );
  await t.test(
    "supplied supplier workbook import and safe ordering",
    { skip: !process.env.SUPPLIER_IMPORT_FILE },
    async () => {
      page.setDefaultTimeout(30000);
      await page
        .locator("#file-input")
        .setInputFiles(process.env.SUPPLIER_IMPORT_FILE);
      await page
        .getByRole("button", { name: "Заменить данные", exact: true })
        .click();
      await page.locator("#modal").waitFor({ state: "hidden" });
      await loaded();
      await page
        .locator("#source-notice")
        .getByText("IEK / Systeme Electric · Алматы", { exact: true })
        .waitFor();
      assert.equal(await page.locator("table tbody tr").count(), 100);
      assert.equal(await page.locator("#export-button").isDisabled(), true);
      await page.getByLabel("Поиск товаров").fill("130200015_");
      assert.equal(await page.locator("table tbody tr").count(), 1);
      await page
        .getByRole("button", { name: "Расчёт для", exact: false })
        .click();
      await page
        .locator("#modal")
        .getByText("43,2 / 0", { exact: true })
        .waitFor();
      await page.getByRole("button", { name: "Готово", exact: true }).click();
      await page.getByLabel("Поиск товаров").fill("");
      const firstPage = await page
        .locator("table tbody tr")
        .first()
        .innerText();
      await page.locator("[data-action='next-page']").click();
      assert.notEqual(
        await page.locator("table tbody tr").first().innerText(),
        firstPage,
      );
      await page
        .getByRole("button", { name: "Параметры расчёта", exact: true })
        .click();
      await page.locator("input[name='lead-0']").fill("14");
      await page.locator("input[name='lead-1']").fill("7");
      await page
        .getByRole("button", { name: "Применить и рассчитать" })
        .click();
      await page.locator("#modal").waitFor({ state: "hidden" });
      await loaded();
      assert.equal(await page.locator("#export-button").isDisabled(), false);
      const planning = await api("recommendations", {
        method: "POST",
        body: JSON.stringify({
          as_of: "2026-09-23",
          lookback_days: 90,
          review_period_days: 7,
          safety_stock_days: 7,
        }),
      });
      const result = await planning.json();
      assert.ok(result.orders.length > 0);
      assert.ok(
        result.orders.every((order) => order.supplier_id === "systeme"),
      );
      assert.ok(
        result.products.some(
          (p) => p.blocked && p.warnings.includes("stock_snapshot_outdated"),
        ),
      );
      assert.deepEqual(errors, []);
    },
  );
  await t.test("real Excel SKU: complete audit and export", async () => {
    await page.getByRole("button", { name: "Пример расчёта", exact: true }).click();
    await page.getByRole("button", { name: "Заменить данные", exact: true }).click();
    await page.locator("#modal").waitFor({ state: "hidden" });
    await loaded();
    await page.getByRole("button", { name: "Пример расчёта", exact: true }).click();
    await page.locator("#calculation-trace").waitFor();
    assert.match(await page.locator("#modal-title").innerText(), /Реле напряжения/);
    const trace = await page.locator("#calculation-trace").innerText();
    for (const text of ["42", "30,67", "47,15", "5,19", "1,38", "MOQ", "2 ед."]) {
      assert.ok(trace.includes(text), `missing trace value: ${text}`);
    }
    await page.locator("#history-trace summary").click();
    assert.equal(await page.locator("#history-trace tbody tr").count(), 12);
    assert.ok((await page.locator("#modal-body").innerText()).includes("01.09.2026"));
    if (process.env.UI_SCREENSHOT_DIR) {
      await page.screenshot({path: join(process.env.UI_SCREENSHOT_DIR, "real-sku-trace.png"), fullPage: true});
    }
    await page.locator("#modal").getByRole("button", {name:"Готово",exact:true}).click();
    assert.equal(await page.locator("#table-content tbody tr").count(), 1);
    assert.match(await page.locator("#table-content").innerText(), /IVR21-1-25/);
    // The embedded snapshot stores the exact 1C code in product_id, while
    // direct imports also supply internal_code. Both must remain searchable.
    await page.getByLabel("Поиск товаров").fill("010400929_");
    assert.equal(await page.locator("#table-count").innerText(), "1");
    assert.match(await page.locator("#table-content").innerText(), /IVR21-1-25/);
    await page.locator(".nav-item[data-view='shipments']").click();
    assert.ok(Number(await page.locator("#table-count").innerText()) > 0);
    assert.match(await page.locator("#table-content").innerText(), /IVR21-1-25/);
    await page.locator(".nav-item[data-view='overview']").click();
    const downloadPromise = page.waitForEvent("download");
    await page.locator("#export-button").click();
    const download = await downloadPromise;
    const csv = await readFile(await download.path(), "utf8");
    assert.ok(csv.includes("IVR21-1-25"));
    assert.ok(csv.includes("после компенсации"));
    assert.deepEqual(errors, [], "browser errors or CSP violations");

    await page.getByLabel("Поиск товаров").fill("");
    await page.locator(".tab[data-filter='all']").click();
    assert.equal(await page.locator("#table-count").innerText(), "3907");
    const recommendations = await api("recommendations", {
      method: "POST",
      body: JSON.stringify({ as_of: "2026-09-23", lookback_days: 365, review_period_days: 14, safety_stock_days: 7 }),
    });
    const { products } = await recommendations.json();
    const spikes = products.reduce((sum, product) => sum + product.adjustments.filter((adjustment) => adjustment.reason === "sales_spike").length, 0);
    const spikeCard = page.locator(".stat-card").filter({ hasText: "Всплесков исключено" });
    assert.equal(await spikeCard.locator(".stat-number").innerText(), `${new Intl.NumberFormat("ru-RU").format(spikes)}корректировок`);
    const firstSummary = page.locator("#table-content summary").first();
    const productID = await firstSummary.locator("..").getAttribute("data-explanation");
    const product = products.find((line) => line.product_id === productID);
    assert.ok(product.audit, "use an actual monthly calculation for the regression");
    await checkExplanation(firstSummary, product.explanation);
    const needed = products.filter((product) => product.order_quantity > 0).length;
    await page.locator(".tab[data-filter='needed']").click();
    assert.equal(Number(await page.locator("#table-count").innerText()), needed);
    await page.locator(".tab[data-filter='attention']").click();
    const attention = Number(await page.locator("#table-count").innerText());
    assert.ok(attention > 0 && attention < 3907, "shared Excel notes must not put every product in attention");
    t.diagnostic(`Excel tabs: needed=${needed}, all=3907, attention=${attention}`);
  });

  await t.test("purchase tabs distinguish orders, problems and healthy stock", async () => {
    const initial = await snapshot();
    const response = await api("recommendations", {
      method: "POST",
      body: JSON.stringify({ as_of: "2026-09-23", lookback_days: 365, review_period_days: 14, safety_stock_days: 7 }),
    });
    const result = await response.json();
    const cases = [
      { id: "healthy" },
      { id: "information", warnings: [
        "Использовано полных месяцев: 12. Пустые ячейки сводных таблиц приняты за 0; отсутствие строки остатка не считается подтверждённым дефицитом.",
        "Остаток: месячный срез 01.09.2026, не текущая инвентаризация; проверьте перед заказом. Срок новой поставки отсутствует в источниках: принято 14 дней. ",
        "Остаток датирован 2026-09-01; выбранная дата не восстанавливает движение склада.",
      ] },
      { id: "purchase", order_quantity: 5 },
      { id: "warning", warnings: ["overdue_shipments_excluded"] },
      { id: "risk", order_quantity: 2, warnings: ["insufficient_supply_during_lead_time"] },
      { id: "adjusted", adjustments: [{ date: "2026-08-01", original_quantity: 100, used_quantity: 10, reason: "sales_spike" }] },
      { id: "blocked", blocked: true },
      { id: "review", review_reasons: ["missing_order_rules"] },
      { id: "text-warning", warnings: ["Остаток отсутствует: для предварительного расчёта принят 0, требуется проверка."] },
      { id: "mixed-warning", warnings: ["Остаток: месячный срез 01.09.2026, не текущая инвентаризация; проверьте перед заказом. MOQ/кратность не подтверждены: расчёт по 1 единице. Срок новой поставки отсутствует в источниках: принято 14 дней."] },
      { id: "stock-warning", warnings: ["stock_snapshot_outdated"] },
      { id: "lead-warning", warnings: ["lead_time_unconfirmed"] },
      { id: "unknown-warning", warnings: ["new_supplier_problem"] },
    ];
    const fixture = structuredClone(initial.data);
    fixture.data.products = cases.map(({ id, review_reasons = [] }) => ({
      ...initial.data.data.products[0], id, name: id, sku: id, review_reasons,
    }));
    fixture.data.stock = cases.map(({ id }) => ({ product_id: id, on_hand: 100, reserved: 0 }));
    fixture.data.sales = [];
    fixture.data.monthly = [];
    fixture.data.shipments = [];
    const template = result.products[0];
    result.products = cases.map(({ id, review_reasons, ...changes }) => ({
      ...template, product_id: id, name: id, sku: id,
      supplier_id: fixture.data.products[0].supplier_id,
      order_quantity: 0, blocked: false, warnings: [], adjustments: [],
      ...changes,
    }));
    result.orders = [];
    const datasetURL = "**/api/v1/dataset";
    const recommendationsURL = "**/api/v1/recommendations";
    await page.route(datasetURL, (route) => route.fulfill({ json: fixture, headers: { ETag: initial.etag } }));
    await page.route(recommendationsURL, (route) => route.fulfill({ json: result }));
    const visibleIDs = () => page.locator("#table-content [data-detail]").evaluateAll(
      (buttons) => buttons.map((button) => button.dataset.detail).sort(),
    );
    const checkTab = async (filter, expected) => {
      await page.locator(`.tab[data-filter='${filter}']`).click();
      assert.deepEqual(await visibleIDs(), [...expected].sort(), filter);
      assert.equal(await page.locator("#table-count").innerText(), String(expected.length));
      assert.equal(await page.locator(`.tab[data-filter='${filter}']`).getAttribute("aria-pressed"), "true");
    };
    try {
      await page.getByLabel("Поиск товаров").fill("");
      await page.getByLabel("Поставщик", { exact: true }).selectOption("");
      await page.locator("#calculate-button").click();
      await loaded();
      await checkTab("needed", ["purchase", "risk"]);
      await checkTab("all", cases.map(({ id }) => id));
      assert.equal(await page.locator("#table-content tr").filter({ has: page.locator("[data-detail='healthy']") }).locator(".badge").innerText(), "Запас в норме");
      await page.locator("[data-detail='information']").click();
      const details = await page.locator("#modal-body").innerText();
      for (const note of cases.find(({ id }) => id === "information").warnings) {
        assert.ok(details.includes(note.trim()), "informational notes remain in product details");
      }
      await page.locator("#modal").getByRole("button", { name: "Готово", exact: true }).click();
      await checkTab("attention", ["warning", "risk", "adjusted", "blocked", "review", "text-warning", "mixed-warning", "stock-warning", "lead-warning", "unknown-warning"]);
      await page.getByLabel("Поиск товаров").fill("healthy");
      assert.deepEqual(await visibleIDs(), []);
      await page.getByLabel("Поиск товаров").fill("");
      await checkTab("needed", ["purchase", "risk"]);
      assert.deepEqual(errors, [], "browser errors or CSP violations");
    } finally {
      await page.unroute(datasetURL);
      await page.unroute(recommendationsURL);
      await page.locator("#calculate-button").click();
      await loaded();
    }
    assert.deepEqual(await snapshot(), initial, "tab filtering must not modify warehouse data");
  });

});
