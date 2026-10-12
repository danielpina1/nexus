// Renders the sketch to PNGs with Playwright (Chromium).
//
//   npm i --no-save playwright@1.56.1          (in a scratch dir, not in web/)
//   NODE_PATH=<scratch>/node_modules PLAYWRIGHT_BROWSERS_PATH=/opt/pw-browsers node design/ios-sketch/render.cjs
//
// Output: design/ios-sketch/png/<screen>.png (light, 2x), png/dark/<screen>.png (dark, 1x),
//         png/overview-light.png and png/overview-dark.png (whole grid, 1x).
const path = require("path");
const fs = require("fs");
const { chromium } = require("playwright");

const dir = __dirname;
const out = path.join(dir, "png");
const url = (q = "") => "file://" + path.join(dir, "index.html") + q;

(async () => {
  fs.mkdirSync(path.join(out, "dark"), { recursive: true });
  const executablePath = process.env.CHROME_PATH || undefined;
  const browser = await chromium.launch({ executablePath });

  async function shots(theme, scale, target) {
    const page = await browser.newPage({ viewport: { width: 1600, height: 1000 }, deviceScaleFactor: scale });
    await page.goto(url(theme === "dark" ? "?theme=dark" : ""));
    await page.waitForLoadState("networkidle");
    await page.evaluate(() => document.fonts.ready);
    const ids = await page.$$eval("figure.shot", (els) => els.map((e) => e.id));
    for (const id of ids) {
      const el = await page.$(`#${id} .capture`);
      await el.screenshot({ path: path.join(target, `${id}.png`), omitBackground: false });
    }
    await page.close();
    return ids;
  }

  async function overview(theme) {
    const page = await browser.newPage({ viewport: { width: 1840, height: 1000 }, deviceScaleFactor: 1 });
    await page.goto(url(theme === "dark" ? "?theme=dark" : ""));
    await page.waitForLoadState("networkidle");
    await page.evaluate(() => document.fonts.ready);
    await page.screenshot({ path: path.join(out, `overview-${theme}.png`), fullPage: true });
    await page.close();
  }

  const ids = await shots("light", Number(process.env.SCALE || 2), out);
  await shots("dark", 1, path.join(out, "dark"));
  await overview("light");
  await overview("dark");
  await browser.close();
  console.log("rendered", ids.length, "screens");
})();
