// scripts/screenshots/capture.js — capture the review page, the mission HUD and the performance page for the docs.
//
// Usage: node capture.js <base-url> <output-dir>
//
// Drives headless Chrome with playwright-core. The browser is the system
// Chrome (or CHROME_PATH); playwright-core downloads no browser of its own.
// Every shot is taken at device scale factor 2 so it stays sharp on high-DPI
// screens. The list of shots is SHOTS below: add one there and re-run
// `make screenshots`.
const { chromium } = require('playwright-core');
const fs = require('fs');
const path = require('path');

const [base, outDir] = process.argv.slice(2);
if (!base || !outDir) { console.error('usage: node capture.js <base-url> <output-dir>'); process.exit(64); }

const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

// name, theme, viewport, tab (a /triage tab), mission (a /missions title to
// open) or page (another path, such as /performance), and an optional step run
// after the page loads.
const SHOTS = [
  { name: 'pending-light', theme: 'light', viewport: DESKTOP, tab: 'pending' },
  { name: 'pending-dark', theme: 'dark', viewport: DESKTOP, tab: 'pending' },
  { name: 'browse-light', theme: 'light', viewport: DESKTOP, tab: 'browse',
    after: async (page) => { await page.click('#view .seg button[data-st=""]'); await page.waitForTimeout(400); } },
  { name: 'directives-light', theme: 'light', viewport: DESKTOP, tab: 'directives' },
  { name: 'directives-dark', theme: 'dark', viewport: DESKTOP, tab: 'directives' },
  { name: 'runs-light', theme: 'light', viewport: DESKTOP, tab: 'runs' },
  { name: 'missions-light', theme: 'light', viewport: DESKTOP, mission: 'Retry transient provider errors' },
  { name: 'missions-dark', theme: 'dark', viewport: DESKTOP, mission: 'Retry transient provider errors' },
  { name: 'missions-steps-light', theme: 'light', viewport: DESKTOP, mission: 'Retry transient provider errors',
    after: async (page) => { await showMissionSection(page, { tab: 'steps', heading: 'Steps', must: '.steps' }); } },
  { name: 'missions-phone-light', theme: 'light', viewport: PHONE, mission: 'Retry transient provider errors',
    after: async (page) => { await showMissionHead(page); } },
  // The set-aside mission from demo.json: its Grade section carries the large N/A badge.
  { name: 'missions-na-light', theme: 'light', viewport: DESKTOP, mission: 'Dry run of the release checklist',
    after: async (page) => { await showMissionSection(page, { heading: 'Grade', must: '.verdict', text: 'N/A' }); } },
  { name: 'performance-light', theme: 'light', viewport: DESKTOP, page: '/performance' },
  { name: 'performance-dark', theme: 'dark', viewport: DESKTOP, page: '/performance' },
  { name: 'phone-light', theme: 'light', viewport: PHONE, tab: 'pending',
    // Scroll past the stacked category list so the first group sits just
    // below the sticky header, rather than under it.
    after: async (page) => {
      await page.evaluate(() => {
        const header = document.querySelector('header.top').offsetHeight;
        window.scrollTo(0, document.querySelector('#view').getBoundingClientRect().top + window.scrollY - header);
      });
      await page.waitForTimeout(300);
    } },
];

// The lowest edge, in CSS pixels, of the bars that stick to the top of the
// viewport: the header, and the mission tab bar where it is sticky (on a phone
// it is static and is not counted). A heading must sit below this edge to be
// readable in a shot.
async function stickyCover(page) {
  return page.evaluate(() => {
    const bars = [...document.querySelectorAll('header.top, #mtabs')]
      .filter(el => getComputedStyle(el).position === 'sticky' && el.getClientRects().length > 0);
    return Math.max(0, ...bars.map(el => parseFloat(getComputedStyle(el).top) + el.offsetHeight));
  });
}

// Makes a mission tab current, when one is given, then scrolls the mission view
// so the heading of the section named by `heading` sits just below the sticky
// bars. The section is found by its heading, never by its position: the panes
// hide the sections of other tabs, so a position can name a hidden one. The shot
// fails unless the section is visible, its heading and its `must` element are on
// screen below the sticky bars, and, when `text` is given, the section contains it.
async function showMissionSection(page, { tab, heading, must, text }) {
  if (tab) {
    await page.click(`#mtabs button[data-mt="${tab}"]`);
    await page.waitForTimeout(300);
  }
  const covered = await stickyCover(page);
  await page.evaluate(({ heading, must, text, covered }) => {
    const sec = [...document.querySelectorAll('#view section.sec')]
      .find(s => (s.querySelector('h3')?.childNodes[0]?.textContent || '').trim() === heading);
    if (!sec || sec.hidden || sec.getClientRects().length === 0) throw new Error(`no visible "${heading}" section`);
    const h3 = sec.querySelector('h3');
    window.scrollTo(0, h3.getBoundingClientRect().top + window.scrollY - covered - 12);
    const below = el => {
      const r = el.getBoundingClientRect();
      return r.height > 0 && r.top >= covered && r.top < window.innerHeight;
    };
    if (!below(h3)) throw new Error(`the "${heading}" heading is clipped by the sticky bars or is off screen`);
    const target = must ? sec.querySelector(must) : null;
    if (must && (!target || !below(target))) throw new Error(`"${heading}" ${must} is not on screen below the sticky bars`);
    if (text && !sec.innerText.includes(text)) throw new Error(`the "${heading}" section does not contain "${text}"`);
  }, { heading, must, text, covered });
  await page.waitForTimeout(300);
}

// Scrolls the mission view so its head (.head, inside #view) sits just below the
// sticky bars. Fails unless the head is on screen there afterwards.
async function showMissionHead(page) {
  const covered = await stickyCover(page);
  await page.evaluate(covered => {
    const view = document.querySelector('#view');
    const head = view.querySelector('.head');
    if (!head || head.getClientRects().length === 0) throw new Error('no visible mission head');
    window.scrollTo(0, view.getBoundingClientRect().top + window.scrollY - covered - 12);
    const r = head.getBoundingClientRect();
    if (r.bottom <= covered || r.top >= window.innerHeight) throw new Error('the mission head is not on screen below the sticky bars');
  }, covered);
  await page.waitForTimeout(300);
}

function chromePath() {
  if (process.env.CHROME_PATH) return process.env.CHROME_PATH;
  const candidates = [
    '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
    '/Applications/Chromium.app/Contents/MacOS/Chromium',
    '/usr/bin/google-chrome', '/usr/bin/google-chrome-stable', '/usr/bin/chromium', '/usr/bin/chromium-browser',
  ];
  return candidates.find(p => fs.existsSync(p));
}

(async () => {
  const executablePath = chromePath();
  if (!executablePath) {
    console.error('No Chrome or Chromium found. Install one, or set CHROME_PATH to its executable.');
    process.exit(2);
  }
  fs.mkdirSync(outDir, { recursive: true });
  const browser = await chromium.launch({ executablePath, headless: true });
  const errors = [];
  for (const shot of SHOTS) {
    const context = await browser.newContext({ viewport: shot.viewport, deviceScaleFactor: 2, colorScheme: shot.theme });
    // A filled-in approver, as an operator would have it; the theme comes from the URL.
    await context.addInitScript(() => { try { localStorage.setItem('triage.approver', 'Operator'); } catch (e) {} });
    const page = await context.newPage();
    page.on('pageerror', e => errors.push(`${shot.name}: ${e.message}`));
    page.on('console', m => { if (m.type() === 'error') errors.push(`${shot.name}: ${m.text()}`); });
    if (shot.page) {
      await page.goto(`${base}${shot.page}?theme=${shot.theme}`);
      await page.waitForSelector('#view .card, #view table, #view .empty');
      await page.waitForTimeout(300);
    } else if (shot.mission) {
      await page.goto(`${base}/missions?theme=${shot.theme}`);
      await page.click(`.mrow:has-text(${JSON.stringify(shot.mission)})`);
      await page.waitForSelector('#view .head');
      await page.waitForTimeout(300);
    } else {
      await page.goto(`${base}/triage?theme=${shot.theme}`);
      await page.waitForSelector('#view .card, #view table, #view .empty');
    }
    if (shot.tab && shot.tab !== 'pending') {
      await page.click(`#tabs button[data-tab="${shot.tab}"]`);
      await page.waitForTimeout(500);
      await page.waitForSelector('#view .card, #view table, #view .empty');
    }
    if (shot.after) await shot.after(page);
    if ((await page.locator('#view').innerText()).includes('Error:')) errors.push(`${shot.name}: the page shows an error`);
    const file = path.join(outDir, `${shot.name}.png`);
    await page.screenshot({ path: file });
    console.log(`[screenshots] ${path.relative(process.cwd(), file)}  ${(fs.statSync(file).size / 1024).toFixed(0)} KB`);
    await context.close();
  }
  await browser.close();
  if (errors.length) { console.error('[screenshots] page errors:\n  ' + errors.join('\n  ')); process.exit(1); }
})().catch(e => { console.error(e); process.exit(1); });
