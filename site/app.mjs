const systemTheme = matchMedia('(prefers-color-scheme: dark)');
const button = document.querySelector('#theme');
let explicitTheme = false;

function theme(dark) {
  document.documentElement.dataset.mode = dark ? 'dark' : 'light';
  button.setAttribute('aria-label', `Use ${dark ? 'light' : 'dark'} theme`);
  button.querySelector('use').setAttribute('href', dark ? '#i-sun' : '#i-moon');
  document.querySelector('meta[name="theme-color"]').content = getComputedStyle(document.documentElement).getPropertyValue('--bg').trim();
}

button.addEventListener('click', () => {
  explicitTheme = true;
  theme(document.documentElement.dataset.mode !== 'dark');
});
systemTheme.addEventListener('change', event => {
  if (!explicitTheme) theme(event.matches);
});
theme(systemTheme.matches);
button.hidden = false;
