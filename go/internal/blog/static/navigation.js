// Keep navigation available without JavaScript; collapse it only after initialization.
(() => {
  document.documentElement.setAttribute('data-js', '');
  document.addEventListener('DOMContentLoaded', () => {
    const button = document.querySelector('.header-burger');
    const menu = document.querySelector('#header-menu');
    button.addEventListener('click', () => {
      const open = menu.classList.toggle('header-menu-visible');
      button.setAttribute('aria-expanded', String(open));
      button.setAttribute('aria-label', open ? 'Закрыть меню' : 'Открыть меню');
    });
  });
})();
