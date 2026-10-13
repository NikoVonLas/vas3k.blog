// Match the upstream theme choice, including persistence and the OS default.
(() => {
  const root = document.documentElement;
  const system = window.matchMedia('(prefers-color-scheme: dark)');
  let preference;
  try { preference = localStorage.getItem('theme'); } catch (_) {}
  const valid = value => value === 'dark' || value === 'light';
  if (!valid(preference)) preference = null;
  const apply = theme => {
    root.setAttribute('theme', theme);
    const button = document.querySelector('.theme-toggle');
    if (button) {
      button.setAttribute('aria-pressed', String(theme === 'dark'));
      button.textContent = theme === 'dark' ? '☀' : '☾';
      button.title = theme === 'dark' ? 'Включить светлую тему' : 'Включить тёмную тему';
    }
  };
  root.setAttribute('data-js', '');
  apply(preference || (system.matches ? 'dark' : 'light'));
  system.addEventListener('change', event => {
    if (!preference) apply(event.matches ? 'dark' : 'light');
  });
  window.addEventListener('storage', event => {
    if (event.key === 'theme') {
      preference = valid(event.newValue) ? event.newValue : null;
      apply(preference || (system.matches ? 'dark' : 'light'));
    }
  });
  document.addEventListener('DOMContentLoaded', () => {
    const button = document.querySelector('.theme-toggle');
    button.hidden = false;
    apply(root.getAttribute('theme'));
    button.addEventListener('click', () => {
      preference = root.getAttribute('theme') === 'dark' ? 'light' : 'dark';
      try { localStorage.setItem('theme', preference); } catch (_) {}
      apply(preference);
    });
    const menuButton = document.querySelector('.header-burger');
    const menu = document.querySelector('#header-menu');
    menuButton.addEventListener('click', () => {
      const open = menu.classList.toggle('header-menu-visible');
      menuButton.setAttribute('aria-expanded', String(open));
      menuButton.setAttribute('aria-label', open ? 'Закрыть меню' : 'Открыть меню');
    });
  });
})();
