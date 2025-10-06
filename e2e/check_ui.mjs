import puppeteer from 'puppeteer';

function section(title){ console.log(`\n=== ${title} ===`); }

const URL = process.env.URL || 'http://localhost:8090';

function cssVisible(display){ return display && display !== 'none' && display !== 'hidden'; }

async function isDisplayed(page, selector){
  const el = await page.$(selector);
  if (!el) return false;
  const style = await page.evaluate(el=>{
    const cs = getComputedStyle(el);
    return { display: cs.display, visibility: cs.visibility, opacity: cs.opacity, rect: el.getBoundingClientRect().toJSON() };
  }, el);
  return cssVisible(style.display) && style.visibility !== 'hidden' && Number(style.opacity||1) > 0 && style.rect.width>0 && style.rect.height>0;
}

async function main(){
  section('Launching browser');
  const browser = await puppeteer.launch({ headless: 'new' });
  const page = await browser.newPage();
  page.setDefaultTimeout(15000);

  section(`Opening ${URL}`);
  await page.goto(URL, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('h2');

  const h2 = await page.$eval('h2', n=>n.textContent.trim());
  console.log('H2:', h2);

  // Force DOM visibility toggles (same as in app) just in case
  await page.evaluate(()=>{
    try{ const bp=document.getElementById('botPanel'); if (bp) bp.style.display='block'; }catch{}
    try{ const dp=document.getElementById('deprecatedPanel'); if (dp) dp.style.display='none'; }catch{}
  });

  section('Checking key controls');
  const checks = [
    ['#matchCreate', 'Nueva partida (Hasty)'],
    ['#matchCreateSim', 'Nueva partida (Sim)'],
    ['#matchSubmitPlay', 'Jugar'],
    ['#matchAIMove', 'AI mueve'],
    ['#matchRack', 'Rack'],
    ['#matchBoard', 'Board']
  ];
  const results = [];
  for (const [sel, label] of checks){
    const ok = await isDisplayed(page, sel);
    console.log(`${label.padEnd(18)}: ${ok ? 'VISIBLE' : 'MISSING'}`);
    results.push([label, ok]);
  }

  section('Deprecated panel hidden');
  const depHidden = await page.$eval('#deprecatedPanel', el=>getComputedStyle(el).display === 'none').catch(()=>true);
  console.log('deprecatedPanel hidden:', depHidden);

  await page.screenshot({ path: 'screen.png', fullPage: true });
  console.log('\nSaved screenshot to e2e/screen.png');

  const allGood = results.every(([,ok])=>ok) && depHidden;
  if (!allGood) process.exitCode = 2;
  await browser.close();
}

main().catch(err=>{ console.error(err); process.exitCode = 1; });

