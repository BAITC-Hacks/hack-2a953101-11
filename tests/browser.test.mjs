import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, mkdir, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import test from "node:test";
import { chromium } from "playwright";

// Runs against its own backend process and temporary warehouse, never user data.
test("purchasing dashboard end-to-end", { timeout: 90000 }, async (t) => {
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
        "3дней",
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
    "inventory, shipments, mobile navigation and no overflow",
    async () => {
      await page
        .getByRole("button", { name: "Товары и остатки", exact: true })
        .click();
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
    const downloadPromise = page.waitForEvent("download");
    await page.locator("#export-button").click();
    const download = await downloadPromise;
    const csv = await readFile(await download.path(), "utf8");
    assert.ok(csv.includes("IVR21-1-25"));
    assert.ok(csv.includes("после компенсации"));
    assert.deepEqual(errors, [], "browser errors or CSP violations");
  });

});
